// Copyright 2024 Google LLC
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

package bufferedwrites

import (
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/googlecloudplatform/gcsfuse/v2/internal/block"
	"github.com/googlecloudplatform/gcsfuse/v2/internal/storage/gcs"
	storagemock "github.com/googlecloudplatform/gcsfuse/v2/internal/storage/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/sync/semaphore"
)

const (
	blockSize       = 1024
	maxBlocks int64 = 5
)

type UploadHandlerTest struct {
	uh         *UploadHandler
	blockPool  *block.BlockPool
	mockBucket *storagemock.TestifyMockBucket
	suite.Suite
}

func TestUploadHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(UploadHandlerTest))
}

func (t *UploadHandlerTest) SetupTest() {
	t.mockBucket = new(storagemock.TestifyMockBucket)
	var err error
	t.blockPool, err = block.NewBlockPool(blockSize, maxBlocks, semaphore.NewWeighted(maxBlocks))
	require.NoError(t.T(), err)
	t.uh = newUploadHandler("testObject", t.mockBucket, maxBlocks, t.blockPool.FreeBlocksChannel(), blockSize)
}

func (t *UploadHandlerTest) TestMultipleBlockUpload() {
	// Create some blocks.
	var blocks []block.Block
	for i := 0; i < 5; i++ {
		b, err := t.blockPool.Get()
		require.NoError(t.T(), err)
		blocks = append(blocks, b)
	}
	// CreateObjectChunkWriter -- should be called once.
	writer := &storagemock.Writer{}
	mockObj := &gcs.Object{}
	t.mockBucket.On("CreateObjectChunkWriter", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(writer, nil)
	t.mockBucket.On("FinalizeUpload", mock.Anything, writer).Return(mockObj, nil)

	// Upload the blocks.
	for _, b := range blocks {
		err := t.uh.Upload(b)
		require.NoError(t.T(), err)
	}

	// Finalize.
	obj, err := t.uh.Finalize()
	require.NoError(t.T(), err)
	require.NotNil(t.T(), obj)
	assert.Equal(t.T(), mockObj, obj)
	// The blocks should be available on the free channel for reuse.
	for _, expect := range blocks {
		got := <-t.uh.freeBlocksCh
		assert.Equal(t.T(), expect, got)
	}
	assertAllBlocksProcessed(t.T(), t.uh)
}

func (t *UploadHandlerTest) TestUploadWhenCreateObjectWriterFails() {
	// Create a block.
	b, err := t.blockPool.Get()
	require.NoError(t.T(), err)
	// CreateObjectChunkWriter -- should be called once.
	t.mockBucket.On("CreateObjectChunkWriter", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("taco"))

	// Upload the block.
	err = t.uh.Upload(b)

	require.Error(t.T(), err)
	assert.ErrorContains(t.T(), err, "createObjectWriter")
	assert.ErrorContains(t.T(), err, "taco")
}

func (t *UploadHandlerTest) TestFinalizeWithWriterAlreadyPresent() {
	writer := &storagemock.Writer{}
	mockObj := &gcs.Object{}
	t.mockBucket.On("FinalizeUpload", mock.Anything, writer).Return(mockObj, nil)
	t.uh.writer = writer

	obj, err := t.uh.Finalize()

	require.NoError(t.T(), err)
	require.NotNil(t.T(), obj)
	assert.Equal(t.T(), mockObj, obj)
}

func (t *UploadHandlerTest) TestFinalizeWithNoWriter() {
	writer := &storagemock.Writer{}
	t.mockBucket.On("CreateObjectChunkWriter", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(writer, nil)
	assert.Nil(t.T(), t.uh.writer)
	mockObj := &gcs.Object{}
	t.mockBucket.On("FinalizeUpload", mock.Anything, writer).Return(mockObj, nil)

	obj, err := t.uh.Finalize()

	require.NoError(t.T(), err)
	require.NotNil(t.T(), obj)
	assert.Equal(t.T(), mockObj, obj)
}

func (t *UploadHandlerTest) TestFinalizeWithNoWriterWhenCreateObjectWriterFails() {
	t.mockBucket.On("CreateObjectChunkWriter", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, fmt.Errorf("taco"))
	assert.Nil(t.T(), t.uh.writer)

	obj, err := t.uh.Finalize()

	require.Error(t.T(), err)
	assert.ErrorContains(t.T(), err, "taco")
	assert.ErrorContains(t.T(), err, "createObjectWriter")
	assert.Nil(t.T(), obj)
}

func (t *UploadHandlerTest) TestFinalizeWhenFinalizeUploadFails() {
	writer := &storagemock.Writer{}
	t.mockBucket.On("CreateObjectChunkWriter", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(writer, nil)
	assert.Nil(t.T(), t.uh.writer)
	mockObj := &gcs.Object{}
	t.mockBucket.On("FinalizeUpload", mock.Anything, writer).Return(mockObj, fmt.Errorf("taco"))

	obj, err := t.uh.Finalize()

	require.Error(t.T(), err)
	assert.Nil(t.T(), obj)
	assert.ErrorContains(t.T(), err, "taco")
	assert.ErrorContains(t.T(), err, "FinalizeUpload failed for object")
}

func (t *UploadHandlerTest) TestUploadSingleBlockThrowsErrorInCopy() {
	// Create a block with test data.
	b, err := t.blockPool.Get()
	require.NoError(t.T(), err)
	err = b.Write([]byte("test data"))
	require.NoError(t.T(), err)
	// CreateObjectChunkWriter -- should be called once.
	writer := &storagemock.Writer{}
	t.mockBucket.On("CreateObjectChunkWriter", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(writer, nil)
	// First write will be an error and Close will be successful.
	writer.On("Write", mock.Anything).Return(0, fmt.Errorf("taco")).Once()

	// Upload the block.
	err = t.uh.Upload(b)

	require.NoError(t.T(), err)
	// Expect an error on the signalUploadFailure channel due to error while copying content to GCS writer.
	assertUploadFailureSignal(t.T(), t.uh)
	assertAllBlocksProcessed(t.T(), t.uh)
	assert.Equal(t.T(), 1, len(t.uh.freeBlocksCh))
}

func (t *UploadHandlerTest) TestUploadMultipleBlocksThrowsErrorInCopy() {
	// Create some blocks.
	var blocks []block.Block
	for i := 0; i < 4; i++ {
		b, err := t.blockPool.Get()
		require.NoError(t.T(), err)
		err = b.Write([]byte("testdata" + strconv.Itoa(i) + " "))
		require.NoError(t.T(), err)
		blocks = append(blocks, b)
	}
	// CreateObjectChunkWriter -- should be called once.
	writer := &storagemock.Writer{}
	t.mockBucket.On("CreateObjectChunkWriter", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(writer, nil)
	// Second write will be an error and rest of the operations will be successful.
	writer.
		On("Write", mock.Anything).Return(10, nil).Once().
		On("Write", mock.Anything).Return(0, fmt.Errorf("taco"))

	// Upload the blocks.
	for _, b := range blocks {
		err := t.uh.Upload(b)
		require.NoError(t.T(), err)
	}

	assertUploadFailureSignal(t.T(), t.uh)
	assertAllBlocksProcessed(t.T(), t.uh)
	assert.Equal(t.T(), 4, len(t.uh.freeBlocksCh))
}

func assertUploadFailureSignal(t *testing.T, handler *UploadHandler) {
	t.Helper()

	select {
	case <-handler.signalUploadFailure:
		break
	case <-time.After(200 * time.Millisecond):
		t.Error("Expected an error on signalUploadFailure channel")
	}
}

func assertAllBlocksProcessed(t *testing.T, handler *UploadHandler) {
	t.Helper()

	// All blocks for upload should have been processed.
	done := make(chan struct{})
	go func() {
		handler.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("Timeout waiting for WaitGroup")
	}
}

func TestSignalUploadFailure(t *testing.T) {
	mockSignalUploadFailure := make(chan error)
	uploadHandler := &UploadHandler{
		signalUploadFailure: mockSignalUploadFailure,
	}

	actualChannel := uploadHandler.SignalUploadFailure()

	assert.Equal(t, mockSignalUploadFailure, actualChannel)
}

func (t *UploadHandlerTest) TestMultipleBlockAwaitBlocksUpload() {
	// Create some blocks.
	var blocks []block.Block
	for i := 0; i < 5; i++ {
		b, err := t.blockPool.Get()
		require.NoError(t.T(), err)
		blocks = append(blocks, b)
	}
	// CreateObjectChunkWriter -- should be called once.
	writer := &storagemock.Writer{}
	t.mockBucket.On("CreateObjectChunkWriter", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(writer, nil)
	// Upload the blocks.
	for _, b := range blocks {
		err := t.uh.Upload(b)
		require.NoError(t.T(), err)
	}

	// AwaitBlocksUpload.
	t.uh.AwaitBlocksUpload()

	assert.Equal(t.T(), 5, len(t.uh.freeBlocksCh))
	assert.Equal(t.T(), 0, len(t.uh.uploadCh))
	assertAllBlocksProcessed(t.T(), t.uh)
}
