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

package gcsx_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/googlecloudplatform/gcsfuse/v2/internal/storage/fake"
	"github.com/googlecloudplatform/gcsfuse/v2/internal/storage/gcs"
	"github.com/googlecloudplatform/gcsfuse/v2/internal/storage/storageutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/context"

	"github.com/googlecloudplatform/gcsfuse/v2/internal/gcsx"
	. "github.com/jacobsa/oglematchers"
	. "github.com/jacobsa/ogletest"
	"github.com/jacobsa/timeutil"
)

func TestPrefixBucket(t *testing.T) { RunTests(t) }

////////////////////////////////////////////////////////////////////////
// Boilerplate
////////////////////////////////////////////////////////////////////////

type PrefixBucketTest struct {
	ctx     context.Context
	prefix  string
	wrapped gcs.Bucket
	bucket  gcs.Bucket
}

var _ SetUpInterface = &PrefixBucketTest{}

func init() { RegisterTestSuite(&PrefixBucketTest{}) }

func (t *PrefixBucketTest) SetUp(ti *TestInfo) {
	var err error

	t.ctx = ti.Ctx
	t.prefix = "foo_"
	t.wrapped = fake.NewFakeBucket(timeutil.RealClock(), "some_bucket", gcs.NonHierarchical)

	t.bucket, err = gcsx.NewPrefixBucket(t.prefix, t.wrapped)
	AssertEq(nil, err)
}

////////////////////////////////////////////////////////////////////////
// Tests
////////////////////////////////////////////////////////////////////////

func (t *PrefixBucketTest) Name() {
	ExpectEq(t.wrapped.Name(), t.bucket.Name())
}

func (t *PrefixBucketTest) NewReader() {
	var err error
	suffix := "taco"
	name := t.prefix + suffix
	contents := "foobar"

	// Create an object through the back door.
	_, err = storageutil.CreateObject(t.ctx, t.wrapped, name, []byte(contents))
	AssertEq(nil, err)

	// Read it through the prefix bucket.
	rc, err := t.bucket.NewReader(
		t.ctx,
		&gcs.ReadObjectRequest{
			Name: suffix,
		})

	AssertEq(nil, err)
	defer rc.Close()

	actual, err := io.ReadAll(rc)
	AssertEq(nil, err)
	ExpectEq(contents, string(actual))
}

func (t *PrefixBucketTest) CreateObject() {
	var err error
	suffix := "taco"
	contents := "foobar"

	// Create the object.
	o, err := t.bucket.CreateObject(
		t.ctx,
		&gcs.CreateObjectRequest{
			Name:            suffix,
			ContentLanguage: "en-GB",
			Contents:        strings.NewReader(contents),
		})

	AssertEq(nil, err)
	ExpectEq(suffix, o.Name)
	ExpectEq("en-GB", o.ContentLanguage)

	// Read it through the back door.
	actual, err := storageutil.ReadObject(t.ctx, t.wrapped, t.prefix+suffix)
	AssertEq(nil, err)
	ExpectEq(contents, string(actual))
}

func (t *PrefixBucketTest) CreateObjectChunkWriterAndFinalizeUpload() {
	var err error
	suffix := "taco"
	content := []byte("foobar")

	// Create the object.
	w, err := t.bucket.CreateObjectChunkWriter(
		t.ctx,
		&gcs.CreateObjectRequest{
			Name:            suffix,
			ContentLanguage: "en-GB",
			Contents:        nil,
		},
		1024, nil)
	AssertEq(nil, err)
	_, err = w.Write(content)
	AssertEq(nil, err)
	o, err := t.bucket.FinalizeUpload(t.ctx, w)

	AssertEq(nil, err)
	ExpectEq(suffix, o.Name)
	ExpectEq("en-GB", o.ContentLanguage)
	// Read it through the back door.
	actual, err := storageutil.ReadObject(t.ctx, t.wrapped, t.prefix+suffix)
	AssertEq(nil, err)
	ExpectEq(string(content), string(actual))
}

func (t *PrefixBucketTest) CopyObject() {
	var err error
	suffix := "taco"
	name := t.prefix + suffix
	contents := "foobar"

	// Create an object through the back door.
	_, err = storageutil.CreateObject(t.ctx, t.wrapped, name, []byte(contents))
	AssertEq(nil, err)

	// Copy it to a new name.
	newSuffix := "burrito"
	o, err := t.bucket.CopyObject(
		t.ctx,
		&gcs.CopyObjectRequest{
			SrcName: suffix,
			DstName: newSuffix,
		})

	AssertEq(nil, err)
	ExpectEq(newSuffix, o.Name)

	// Read it through the back door.
	actual, err := storageutil.ReadObject(t.ctx, t.wrapped, t.prefix+newSuffix)
	AssertEq(nil, err)
	ExpectEq(contents, string(actual))
}

func (t *PrefixBucketTest) ComposeObjects() {
	var err error

	suffix0 := "taco"
	contents0 := "foo"

	suffix1 := "burrito"
	contents1 := "bar"

	// Create two objects through the back door.
	err = storageutil.CreateObjects(
		t.ctx,
		t.wrapped,
		map[string][]byte{
			t.prefix + suffix0: []byte(contents0),
			t.prefix + suffix1: []byte(contents1),
		})

	AssertEq(nil, err)

	// Compose them.
	newSuffix := "enchilada"
	o, err := t.bucket.ComposeObjects(
		t.ctx,
		&gcs.ComposeObjectsRequest{
			DstName: newSuffix,
			Sources: []gcs.ComposeSource{
				{Name: suffix0},
				{Name: suffix1},
			},
		})

	AssertEq(nil, err)
	ExpectEq(newSuffix, o.Name)

	// Read it through the back door.
	actual, err := storageutil.ReadObject(t.ctx, t.wrapped, t.prefix+newSuffix)
	AssertEq(nil, err)
	ExpectEq(contents0+contents1, string(actual))
}

func (t *PrefixBucketTest) StatObject() {
	var err error
	suffix := "taco"
	name := t.prefix + suffix
	contents := "foobar"

	// Create an object through the back door.
	_, err = storageutil.CreateObject(t.ctx, t.wrapped, name, []byte(contents))
	AssertEq(nil, err)

	// Stat it.
	m, _, err := t.bucket.StatObject(
		t.ctx,
		&gcs.StatObjectRequest{
			Name: suffix,
		})

	AssertEq(nil, err)
	AssertNe(nil, m)
	ExpectEq(suffix, m.Name)
	ExpectEq(len(contents), m.Size)
}

func (t *PrefixBucketTest) ListObjects_NoOptions() {
	var err error

	// Create a few objects.
	err = storageutil.CreateObjects(
		t.ctx,
		t.wrapped,
		map[string][]byte{
			t.prefix + "burrito":   []byte(""),
			t.prefix + "enchilada": []byte(""),
			t.prefix + "taco":      []byte(""),
			"some_other":           []byte(""),
		})

	AssertEq(nil, err)

	// List.
	l, err := t.bucket.ListObjects(
		t.ctx,
		&gcs.ListObjectsRequest{})

	AssertEq(nil, err)
	AssertEq("", l.ContinuationToken)
	AssertThat(l.CollapsedRuns, ElementsAre())

	AssertEq(3, len(l.MinObjects))
	ExpectEq("burrito", l.MinObjects[0].Name)
	ExpectEq("enchilada", l.MinObjects[1].Name)
	ExpectEq("taco", l.MinObjects[2].Name)
}

func (t *PrefixBucketTest) ListObjects_Prefix() {
	var err error

	// Create a few objects.
	err = storageutil.CreateObjects(
		t.ctx,
		t.wrapped,
		map[string][]byte{
			t.prefix + "burritn":  []byte(""),
			t.prefix + "burrito0": []byte(""),
			t.prefix + "burrito1": []byte(""),
			t.prefix + "burritp":  []byte(""),
			"some_other":          []byte(""),
		})

	AssertEq(nil, err)

	// List, with a prefix.
	l, err := t.bucket.ListObjects(
		t.ctx,
		&gcs.ListObjectsRequest{
			Prefix: "burrito",
		})

	AssertEq(nil, err)
	AssertEq("", l.ContinuationToken)
	AssertThat(l.CollapsedRuns, ElementsAre())

	AssertEq(2, len(l.MinObjects))
	ExpectEq("burrito0", l.MinObjects[0].Name)
	ExpectEq("burrito1", l.MinObjects[1].Name)
}

func (t *PrefixBucketTest) ListObjects_Delimeter() {
	var err error

	// Create a few objects.
	err = storageutil.CreateObjects(
		t.ctx,
		t.wrapped,
		map[string][]byte{
			t.prefix + "burrito":     []byte(""),
			t.prefix + "burrito_0":   []byte(""),
			t.prefix + "burrito_1":   []byte(""),
			t.prefix + "enchilada_0": []byte(""),
			"some_other":             []byte(""),
		})

	AssertEq(nil, err)

	// List, with a delimiter. Make things extra interesting by using a delimiter
	// that is contained within the bucket prefix.
	AssertNe(-1, strings.IndexByte(t.prefix, '_'))
	l, err := t.bucket.ListObjects(
		t.ctx,
		&gcs.ListObjectsRequest{
			Delimiter: "_",
		})

	AssertEq(nil, err)
	AssertEq("", l.ContinuationToken)

	ExpectThat(l.CollapsedRuns, ElementsAre("burrito_", "enchilada_"))

	AssertEq(1, len(l.MinObjects))
	ExpectEq("burrito", l.MinObjects[0].Name)
}

func (t *PrefixBucketTest) ListObjects_PrefixAndDelimeter() {
	var err error

	// Create a few objects.
	err = storageutil.CreateObjects(
		t.ctx,
		t.wrapped,
		map[string][]byte{
			t.prefix + "burrito":     []byte(""),
			t.prefix + "burrito_0":   []byte(""),
			t.prefix + "burrito_1":   []byte(""),
			t.prefix + "enchilada_0": []byte(""),
			"some_other":             []byte(""),
		})

	AssertEq(nil, err)

	// List, with a delimiter and a prefix. Make things extra interesting by
	// using a delimiter that is contained within the bucket prefix.
	AssertNe(-1, strings.IndexByte(t.prefix, '_'))
	l, err := t.bucket.ListObjects(
		t.ctx,
		&gcs.ListObjectsRequest{
			Delimiter: "_",
			Prefix:    "burrito",
		})

	AssertEq(nil, err)
	AssertEq("", l.ContinuationToken)

	ExpectThat(l.CollapsedRuns, ElementsAre("burrito_"))

	AssertEq(1, len(l.MinObjects))
	ExpectEq("burrito", l.MinObjects[0].Name)
}

func (t *PrefixBucketTest) UpdateObject() {
	var err error
	suffix := "taco"
	name := t.prefix + suffix
	contents := "foobar"

	// Create an object through the back door.
	_, err = storageutil.CreateObject(t.ctx, t.wrapped, name, []byte(contents))
	AssertEq(nil, err)

	// Update it.
	newContentLanguage := "en-GB"
	o, err := t.bucket.UpdateObject(
		t.ctx,
		&gcs.UpdateObjectRequest{
			Name:            suffix,
			ContentLanguage: &newContentLanguage,
		})

	AssertEq(nil, err)
	ExpectEq(suffix, o.Name)
	ExpectEq(newContentLanguage, o.ContentLanguage)
}

func (t *PrefixBucketTest) DeleteObject() {
	var err error
	suffix := "taco"
	name := t.prefix + suffix
	contents := "foobar"

	// Create an object through the back door.
	_, err = storageutil.CreateObject(t.ctx, t.wrapped, name, []byte(contents))
	AssertEq(nil, err)

	// Delete it.
	err = t.bucket.DeleteObject(
		t.ctx,
		&gcs.DeleteObjectRequest{
			Name: suffix,
		})

	AssertEq(nil, err)

	// It should be gone.
	_, _, err = t.wrapped.StatObject(
		t.ctx,
		&gcs.StatObjectRequest{
			Name: name,
		})

	var notFoundErr *gcs.NotFoundError
	ExpectTrue(errors.As(err, &notFoundErr))
}

func TestGetFolder_Prefix(t *testing.T) {
	prefix := "foo_"
	wrapped := fake.NewFakeBucket(timeutil.RealClock(), "some_bucket", gcs.NonHierarchical)
	bucket, err := gcsx.NewPrefixBucket(prefix, wrapped)
	require.Nil(t, err)
	folderName := "taco"
	name := "foo_" + folderName
	ctx := context.Background()
	_, err = wrapped.CreateFolder(ctx, name)
	require.Nil(t, err)

	result, err := bucket.GetFolder(
		ctx,
		folderName)

	assert.Nil(nil, err)
	assert.Equal(t, folderName, result.Name)
}

func TestDeleteFolder(t *testing.T) {
	prefix := "foo_"
	wrapped := fake.NewFakeBucket(timeutil.RealClock(), "some_bucket", gcs.NonHierarchical)
	bucket, err := gcsx.NewPrefixBucket(prefix, wrapped)
	require.Nil(t, err)
	folderName := "taco"
	name := "foo_" + folderName

	ctx := context.Background()
	_, err = wrapped.CreateFolder(ctx, name)
	require.Nil(t, err)

	err = bucket.DeleteFolder(
		ctx,
		folderName)

	if assert.Nil(t, err) {
		_, err = wrapped.GetFolder(
			ctx,
			folderName)
		var notFoundErr *gcs.NotFoundError
		assert.ErrorAs(t, err, &notFoundErr)
	}
}

func TestRenameFolder(t *testing.T) {
	prefix := "foo_"
	var err error
	old_suffix := "test"
	name := prefix + old_suffix
	new_suffix := "new_test"
	wrapped := fake.NewFakeBucket(timeutil.RealClock(), "some_bucket", gcs.NonHierarchical)
	bucket, err := gcsx.NewPrefixBucket(prefix, wrapped)
	require.Nil(t, err)
	ctx := context.Background()
	_, err = wrapped.CreateFolder(ctx, name)
	assert.Nil(t, err)

	f, err := bucket.RenameFolder(ctx, old_suffix, new_suffix)
	assert.Nil(t, err)
	assert.Equal(t, new_suffix, f.Name)

	// New folder should get created
	_, err = bucket.GetFolder(ctx, new_suffix)
	assert.Nil(t, err)
	// Old folder should be gone.
	_, err = bucket.GetFolder(ctx, old_suffix)
	var notFoundErr *gcs.NotFoundError
	assert.True(t, errors.As(err, &notFoundErr))
}

func TestCreateFolder(t *testing.T) {
	prefix := "foo_"
	var err error
	suffix := "test"
	wrapped := fake.NewFakeBucket(timeutil.RealClock(), "some_bucket", gcs.NonHierarchical)
	bucket, err := gcsx.NewPrefixBucket(prefix, wrapped)
	require.NoError(t, err)
	ctx := context.Background()

	f, err := bucket.CreateFolder(ctx, suffix)

	assert.Equal(t, f.Name, suffix)
	assert.NoError(t, err)
	// Folder should get created
	_, err = bucket.GetFolder(ctx, suffix)
	assert.NoError(t, err)
}
