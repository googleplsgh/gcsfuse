// Copyright 2015 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package inode

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/googlecloudplatform/gcsfuse/v2/cfg"
	"github.com/googlecloudplatform/gcsfuse/v2/internal/bufferedwrites"
	"github.com/googlecloudplatform/gcsfuse/v2/internal/contentcache"
	"github.com/googlecloudplatform/gcsfuse/v2/internal/fs/gcsfuse_errors"
	"github.com/googlecloudplatform/gcsfuse/v2/internal/gcsx"
	"github.com/googlecloudplatform/gcsfuse/v2/internal/storage/gcs"
	"github.com/googlecloudplatform/gcsfuse/v2/internal/storage/storageutil"
	"github.com/jacobsa/fuse/fuseops"
	"github.com/jacobsa/syncutil"
	"github.com/jacobsa/timeutil"
	"golang.org/x/net/context"
	"golang.org/x/sync/semaphore"
)

// A GCS object metadata key for file mtimes. mtimes are UTC, and are stored in
// the format defined by time.RFC3339Nano.
const FileMtimeMetadataKey = gcsx.MtimeMetadataKey

type FileInode struct {
	/////////////////////////
	// Dependencies
	/////////////////////////

	bucket     *gcsx.SyncerBucket
	mtimeClock timeutil.Clock

	/////////////////////////
	// Constant data
	/////////////////////////

	id           fuseops.InodeID
	name         Name
	attrs        fuseops.InodeAttributes
	contentCache *contentcache.ContentCache
	// TODO (#640) remove bool flag and refactor contentCache to support two implementations:
	// one implementation with original functionality and one with new persistent disk content cache
	localFileCache bool

	/////////////////////////
	// Mutable state
	/////////////////////////

	// A mutex that must be held when calling certain methods. See documentation
	// for each method.
	mu syncutil.InvariantMutex

	// GUARDED_BY(mu)
	lc lookupCount

	// The source object from which this inode derives.
	//
	// INVARIANT: for non local files,  src.Name == name.GcsObjectName()
	//
	// GUARDED_BY(mu)
	src gcs.MinObject

	// The current content of this inode, or nil if the source object is still
	// authoritative.
	content gcsx.TempFile

	// Has Destroy been called?
	//
	// GUARDED_BY(mu)
	destroyed bool

	// Represents a local file which is not yet synced to GCS.
	local bool

	// Represents if local file has been unlinked.
	unlinked bool

	bwh                *bufferedwrites.BufferedWriteHandler
	writeConfig        *cfg.WriteConfig
	globalMaxBlocksSem *semaphore.Weighted
}

var _ Inode = &FileInode{}

// Create a file inode for the given min object in GCS. The initial lookup count is
// zero.
//
// REQUIRES: m != nil
// REQUIRES: m.Generation > 0
// REQUIRES: m.MetaGeneration > 0
// REQUIRES: len(m.Name) > 0
// REQUIRES: m.Name[len(m.Name)-1] != '/'
func NewFileInode(
	id fuseops.InodeID,
	name Name,
	m *gcs.MinObject,
	attrs fuseops.InodeAttributes,
	bucket *gcsx.SyncerBucket,
	localFileCache bool,
	contentCache *contentcache.ContentCache,
	mtimeClock timeutil.Clock,
	localFile bool,
	writeConfig *cfg.WriteConfig,
	globalMaxBlocksSem *semaphore.Weighted) (f *FileInode) {
	// Set up the basic struct.
	var minObj gcs.MinObject
	if m != nil {
		minObj = *m
	}
	f = &FileInode{
		bucket:             bucket,
		mtimeClock:         mtimeClock,
		id:                 id,
		name:               name,
		attrs:              attrs,
		localFileCache:     localFileCache,
		contentCache:       contentCache,
		src:                minObj,
		local:              localFile,
		unlinked:           false,
		writeConfig:        writeConfig,
		globalMaxBlocksSem: globalMaxBlocksSem,
	}

	f.lc.Init(id)

	// Set up invariant checking.
	f.mu = syncutil.NewInvariantMutex(f.checkInvariants)

	return
}

////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////

// LOCKS_REQUIRED(f.mu)
func (f *FileInode) checkInvariants() {
	if f.destroyed {
		return
	}

	// Make sure the name is legal.
	name := f.Name()
	if !name.IsFile() {
		panic("Illegal file name: " + name.String())
	}

	// INVARIANT: For non-local inodes, src.Name == name
	if !f.IsLocal() && f.src.Name != name.GcsObjectName() {
		panic(fmt.Sprintf(
			"Name mismatch: %q vs. %q",
			f.src.Name,
			name.GcsObjectName(),
		))
	}

	// INVARIANT: content.CheckInvariants() does not panic
	if f.content != nil {
		f.content.CheckInvariants()
	}
}

// LOCKS_REQUIRED(f.mu)
func (f *FileInode) clobbered(ctx context.Context, forceFetchFromGcs bool, includeExtendedObjectAttributes bool) (o *gcs.Object, b bool, err error) {
	// Stat the object in GCS. ForceFetchFromGcs ensures object is fetched from
	// gcs and not cache.
	req := &gcs.StatObjectRequest{
		Name:                           f.name.GcsObjectName(),
		ForceFetchFromGcs:              forceFetchFromGcs,
		ReturnExtendedObjectAttributes: includeExtendedObjectAttributes,
	}
	m, e, err := f.bucket.StatObject(ctx, req)
	if includeExtendedObjectAttributes {
		o = storageutil.ConvertMinObjectAndExtendedObjectAttributesToObject(m, e)
	} else {
		o = storageutil.ConvertMinObjectToObject(m)
	}
	// Special case: "not found" means we have been clobbered.
	var notFoundErr *gcs.NotFoundError
	if errors.As(err, &notFoundErr) {
		err = nil
		if f.IsLocal() {
			// For localFile, it is expected that object doesn't exist in GCS.
			return
		}

		b = true
		return
	}

	// Propagate other errors.
	if err != nil {
		err = fmt.Errorf("StatObject: %w", err)
		return
	}

	// We are clobbered iff the generation doesn't match our source generation.
	oGen := Generation{o.Generation, o.MetaGeneration}
	b = f.SourceGeneration().Compare(oGen) != 0

	return
}

// Open a reader for the generation of object we care about.
func (f *FileInode) openReader(ctx context.Context) (io.ReadCloser, error) {
	rc, err := f.bucket.NewReader(
		ctx,
		&gcs.ReadObjectRequest{
			Name:           f.src.Name,
			Generation:     f.src.Generation,
			ReadCompressed: f.src.HasContentEncodingGzip(),
		})
	// If the object with requested generation doesn't exist in GCS, it indicates
	// a file clobbering scenario. This likely occurred because the file was
	// modified/deleted leading to different generation number.
	var notFoundError *gcs.NotFoundError
	if errors.As(err, &notFoundError) {
		err = &gcsfuse_errors.FileClobberedError{
			Err: fmt.Errorf("NewReader: %w", err),
		}
	}
	if err != nil {
		err = fmt.Errorf("NewReader: %w", err)
	}
	return rc, err
}

// Ensure that content exists and is not stale
//
// LOCKS_REQUIRED(f.mu)
func (f *FileInode) ensureContent(ctx context.Context) (err error) {
	if f.localFileCache {
		// Fetch content from the cache after validating generation numbers again
		// Generation validation first occurs at inode creation/destruction
		cacheObjectKey := &contentcache.CacheObjectKey{BucketName: f.bucket.Name(), ObjectName: f.name.objectName}
		if cacheObject, exists := f.contentCache.Get(cacheObjectKey); exists {
			if cacheObject.ValidateGeneration(f.src.Generation, f.src.MetaGeneration) {
				f.content = cacheObject.CacheFile
				return
			}
		}

		rc, err := f.openReader(ctx)
		if err != nil {
			err = fmt.Errorf("openReader Error: %w", err)
			return err
		}

		// Insert object into content cache
		tf, err := f.contentCache.AddOrReplace(cacheObjectKey, f.src.Generation, f.src.MetaGeneration, rc)
		if err != nil {
			err = fmt.Errorf("AddOrReplace cache error: %w", err)
			return err
		}

		// Update state.
		f.content = tf.CacheFile
	} else {
		// Local filecache is not enabled
		if f.content != nil {
			return
		}

		rc, err := f.openReader(ctx)
		if err != nil {
			err = fmt.Errorf("openReader Error: %w", err)
			return err
		}

		tf, err := f.contentCache.NewTempFile(rc)
		if err != nil {
			err = fmt.Errorf("NewTempFile: %w", err)
			return err
		}
		// Update state.
		f.content = tf
	}

	return
}

////////////////////////////////////////////////////////////////////////
// Public interface
////////////////////////////////////////////////////////////////////////

func (f *FileInode) Lock() {
	f.mu.Lock()
}

func (f *FileInode) Unlock() {
	f.mu.Unlock()
}

func (f *FileInode) ID() fuseops.InodeID {
	return f.id
}

func (f *FileInode) Name() Name {
	return f.name
}

func (f *FileInode) IsLocal() bool {
	return f.local
}

func (f *FileInode) IsUnlinked() bool {
	return f.unlinked
}

func (f *FileInode) Unlink() {
	f.unlinked = true
}

// Source returns a record for the GCS object from which this inode is branched. The
// record is guaranteed not to be modified, and users must not modify it.
//
// LOCKS_REQUIRED(f.mu)
func (f *FileInode) Source() *gcs.MinObject {
	// Make a copy, since we modify f.src.
	o := f.src
	return &o
}

// If true, it is safe to serve reads directly from the object given by
// f.Source(), rather than calling f.ReadAt. Doing so may be more efficient,
// because f.ReadAt may cause the entire object to be faulted in and requires
// the inode to be locked during the read.
//
// LOCKS_REQUIRED(f.mu)
func (f *FileInode) SourceGenerationIsAuthoritative() bool {
	// When streaming writes are enabled, writes are done via bufferedWritesHandler(bwh).
	// Hence checking both f.content & f.bwh to be nil
	return f.content == nil && f.bwh == nil
}

// Equivalent to the generation returned by f.Source().
//
// LOCKS_REQUIRED(f)
func (f *FileInode) SourceGeneration() (g Generation) {
	g.Object = f.src.Generation
	g.Metadata = f.src.MetaGeneration
	return
}

// LOCKS_REQUIRED(f.mu)
func (f *FileInode) IncrementLookupCount() {
	f.lc.Inc()
}

// LOCKS_REQUIRED(f.mu)
func (f *FileInode) DecrementLookupCount(n uint64) (destroy bool) {
	destroy = f.lc.Dec(n)
	return
}

// LOCKS_REQUIRED(f.mu)
func (f *FileInode) Destroy() (err error) {
	f.destroyed = true
	if f.localFileCache {
		cacheObjectKey := &contentcache.CacheObjectKey{BucketName: f.bucket.Name(), ObjectName: f.name.objectName}
		f.contentCache.Remove(cacheObjectKey)
	} else if f.content != nil {
		f.content.Destroy()
	}
	return
}

// LOCKS_REQUIRED(f.mu)
func (f *FileInode) Attributes(
	ctx context.Context) (attrs fuseops.InodeAttributes, err error) {
	attrs = f.attrs

	// Obtain default information from the source object.
	attrs.Mtime = f.src.Updated
	attrs.Size = f.src.Size

	// If the source object has an mtime metadata key, use that instead of its
	// update time.
	// If the file was copied via gsutil, we'll have goog-reserved-file-mtime
	if strTimestamp, ok := f.src.Metadata["goog-reserved-file-mtime"]; ok {
		if timestamp, err := strconv.ParseInt(strTimestamp, 0, 64); err == nil {
			attrs.Mtime = time.Unix(timestamp, 0)
		}
	}

	// Otherwise, if its been synced with gcsfuse before, we'll have gcsfuse_mtime
	if formatted, ok := f.src.Metadata["gcsfuse_mtime"]; ok {
		attrs.Mtime, err = time.Parse(time.RFC3339Nano, formatted)
		if err != nil {
			err = fmt.Errorf("time.Parse(%q): %w", formatted, err)
			return
		}
	}

	// If we've got local content, its size and (maybe) mtime take precedence.
	if f.content != nil {
		var sr gcsx.StatResult
		sr, err = f.content.Stat()
		if err != nil {
			err = fmt.Errorf("stat: %w", err)
			return
		}

		attrs.Size = uint64(sr.Size)
		if sr.Mtime != nil {
			attrs.Mtime = *sr.Mtime
		}
	}

	if f.bwh != nil {
		writeFileInfo := f.bwh.WriteFileInfo()
		attrs.Mtime = writeFileInfo.Mtime
		attrs.Size = uint64(writeFileInfo.TotalSize)
	}

	// We require only that atime and ctime be "reasonable".
	attrs.Atime = attrs.Mtime
	attrs.Ctime = attrs.Mtime

	// If the object has been clobbered, we reflect that as the inode being
	// unlinked.
	_, clobbered, err := f.clobbered(ctx, false, false)
	if err != nil {
		err = fmt.Errorf("clobbered: %w", err)
		return
	}

	attrs.Nlink = 1

	// For local files, also checking if file is unlinked locally.
	if clobbered || (f.IsLocal() && f.IsUnlinked()) {
		attrs.Nlink = 0
	}

	return
}

func (f *FileInode) Bucket() *gcsx.SyncerBucket {
	return f.bucket
}

// Serve a read for this file with semantics matching io.ReaderAt.
//
// The caller may be better off reading directly from GCS when
// f.SourceGenerationIsAuthoritative() is true.
//
// LOCKS_REQUIRED(f.mu)
func (f *FileInode) Read(
	ctx context.Context,
	dst []byte,
	offset int64) (n int, err error) {
	// It is not nil when streaming writes are enabled in 2 scenarios:
	// 1. Local file
	// 2. Empty GCS files and writes are triggered via buffered flow.
	if f.bwh != nil {
		err = fmt.Errorf("cannot read a file when upload in progress")
		return
	}

	// Make sure f.content != nil.
	err = f.ensureContent(ctx)
	if err != nil {
		err = fmt.Errorf("ensureContent: %w", err)
		return
	}

	// Read from the local content, propagating io.EOF.
	n, err = f.content.ReadAt(dst, offset)
	switch {
	case err == io.EOF:
		return

	case err != nil:
		err = fmt.Errorf("content.ReadAt: %w", err)
		return
	}

	return
}

// Serve a write for this file with semantics matching fuseops.WriteFileOp.
//
// LOCKS_REQUIRED(f.mu)
func (f *FileInode) Write(
	ctx context.Context,
	data []byte,
	offset int64) (err error) {
	// For empty GCS files also we will trigger bufferedWrites flow.
	if f.src.Size == 0 && f.writeConfig.ExperimentalEnableStreamingWrites {
		err = f.ensureBufferedWriteHandler()
		if err != nil {
			return
		}
	}

	if f.bwh != nil {
		return f.bwh.Write(data, offset)
	}

	// Make sure f.content != nil.
	err = f.ensureContent(ctx)
	if err != nil {
		err = fmt.Errorf("ensureContent: %w", err)
		return
	}

	// Write to the mutable content. Note that io.WriterAt guarantees it returns
	// an error for short writes.
	_, err = f.content.WriteAt(data, offset)

	return
}

// Set the mtime for this file. May involve a round trip to GCS.
//
// LOCKS_REQUIRED(f.mu)
func (f *FileInode) SetMtime(
	ctx context.Context,
	mtime time.Time) (err error) {
	// When bufferedWritesHandler instance is not nil, set time on bwh.
	// It will not be nil in 2 cases when bufferedWrites are enabled:
	// 1. local files
	// 2. After first write on empty GCS files.
	if f.bwh != nil {
		f.bwh.SetMtime(mtime)
		return
	}

	// If we have a local temp file, stat it.
	var sr gcsx.StatResult
	if f.content != nil {
		sr, err = f.content.Stat()
		if err != nil {
			err = fmt.Errorf("stat: %w", err)
			return
		}
	}

	// 1. If the local content is dirty, simply update its mtime and return. This
	// will cause the object in the bucket to be updated once we sync. If we lose
	// power or something the mtime update will be lost, but so will the file
	// data modifications so this doesn't seem so bad. It's worth saving the
	// round trip to GCS for the common case of Linux writeback caching, where we
	// always receive a setattr request just before a flush of a dirty file.
	//
	// 2. If the file is local, that means its not yet synced to GCS. Just update
	// the mtime locally, it will be synced when the object is created on GCS.
	if sr.Mtime != nil || f.IsLocal() {
		f.content.SetMtime(mtime)
		return
	}

	// Otherwise, update the backing object's metadata.
	formatted := mtime.UTC().Format(time.RFC3339Nano)
	srcGen := f.SourceGeneration()

	req := &gcs.UpdateObjectRequest{
		Name:                       f.src.Name,
		Generation:                 srcGen.Object,
		MetaGenerationPrecondition: &srcGen.Metadata,
		Metadata: map[string]*string{
			FileMtimeMetadataKey: &formatted,
		},
	}

	o, err := f.bucket.UpdateObject(ctx, req)
	if err == nil {
		var minObj gcs.MinObject
		minObjPtr := storageutil.ConvertObjToMinObject(o)
		if minObjPtr != nil {
			minObj = *minObjPtr
		}
		f.src = minObj
		return
	}

	var notFoundErr *gcs.NotFoundError
	if errors.As(err, &notFoundErr) {
		// Special case: silently ignore not found errors, which mean the file has
		// been unlinked.
		err = nil
		return
	}

	var preconditionErr *gcs.PreconditionError
	if errors.As(err, &preconditionErr) {
		// Special case: silently ignore precondition errors, which we also take to
		// mean the file has been unlinked.
		err = nil
		return
	}

	err = fmt.Errorf("UpdateObject: %w", err)
	return
}

// Sync writes out contents to GCS. If this fails due to the generation having been
// clobbered, failure is propagated back to the calling function as an error.
//
// After this method succeeds, SourceGeneration will return the new generation
// by which this inode should be known (which may be the same as before). If it
// fails, the generation will not change.
//
// LOCKS_REQUIRED(f.mu)
func (f *FileInode) Sync(ctx context.Context) (err error) {
	// If we have not been dirtied, there is nothing to do.
	if f.content == nil {
		return
	}

	// When listObjects call is made, we fetch data with projection set as noAcl
	// which means acls and owner properties are not returned. So the f.src object
	// here will not have acl information even though there are acls present on
	// the gcsObject.
	// Hence, we are making an explicit gcs stat call to fetch the latest
	// properties and using that when object is synced below. StatObject by
	// default sets the projection to full, which fetches all the object
	// properties.
	latestGcsObj, isClobbered, err := f.clobbered(ctx, true, true)

	if isClobbered {
		err = &gcsfuse_errors.FileClobberedError{
			Err: err,
		}
	}

	if err != nil {
		return
	}

	// Write out the contents if they are dirty.
	// Object properties are also synced as part of content sync. Hence, passing
	// the latest object fetched from gcs which has all the properties populated.
	newObj, err := f.bucket.SyncObject(ctx, f.Name().GcsObjectName(), latestGcsObj, f.content)

	var preconditionErr *gcs.PreconditionError
	if errors.As(err, &preconditionErr) {
		err = &gcsfuse_errors.FileClobberedError{
			Err: fmt.Errorf("SyncObject: %w", err),
		}
		return
	}

	// Propagate other errors.
	if err != nil {
		err = fmt.Errorf("SyncObject: %w", err)
		return
	}

	// If we wrote out a new object, we need to update our state.
	if newObj != nil && !f.localFileCache {
		var minObj gcs.MinObject
		minObjPtr := storageutil.ConvertObjToMinObject(newObj)
		if minObjPtr != nil {
			minObj = *minObjPtr
		}
		f.src = minObj
		// Convert localFile to nonLocalFile after it is synced to GCS.
		if f.IsLocal() {
			f.local = false
		}
		f.content.Destroy()
		f.content = nil
	}

	return
}

// Truncate the file to the specified size.
//
// LOCKS_REQUIRED(f.mu)
func (f *FileInode) Truncate(
	ctx context.Context,
	size int64) (err error) {
	// Make sure f.content != nil.
	err = f.ensureContent(ctx)
	if err != nil {
		err = fmt.Errorf("ensureContent: %w", err)
		return
	}

	// Call through.
	err = f.content.Truncate(size)

	return
}

// Ensures cache content on read if content cache enabled
func (f *FileInode) CacheEnsureContent(ctx context.Context) (err error) {
	if f.localFileCache {
		err = f.ensureContent(ctx)
	}

	return
}

func (f *FileInode) CreateBufferedOrTempWriter() (err error) {
	// Skip creating empty file when streaming writes are enabled
	if f.local && f.writeConfig.ExperimentalEnableStreamingWrites {
		err = f.ensureBufferedWriteHandler()
		if err != nil {
			return
		}
		return
	}

	// Creating a file with no contents. The contents will be updated with
	// writeFile operations.
	f.content, err = f.contentCache.NewTempFile(io.NopCloser(strings.NewReader("")))
	// Setting the initial mtime to creation time.
	f.content.SetMtime(f.mtimeClock.Now())
	return
}

func (f *FileInode) ensureBufferedWriteHandler() error {
	var err error
	if f.bwh == nil {
		f.bwh, err = bufferedwrites.NewBWHandler(f.name.GcsObjectName(), f.bucket, f.writeConfig.BlockSizeMb, f.writeConfig.MaxBlocksPerFile, f.globalMaxBlocksSem)
		if err != nil {
			return fmt.Errorf("failed to create bufferedWriteHandler: %w", err)
		}
		f.bwh.SetMtime(f.mtimeClock.Now())
	}

	return nil
}
