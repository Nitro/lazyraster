package repository

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type fakePageCacheS3 struct {
	mutex sync.Mutex

	getPayload []byte
	getErr     error
	getKeys    []string

	putErr  error
	puts    map[string][]byte
	putDone chan struct{}
}

func newFakePageCacheS3() *fakePageCacheS3 {
	return &fakePageCacheS3{puts: make(map[string][]byte), putDone: make(chan struct{}, 16)}
}

func (f *fakePageCacheS3) GetObject(
	_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options),
) (*s3.GetObjectOutput, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	f.getKeys = append(f.getKeys, *input.Key)
	if f.getErr != nil {
		return nil, f.getErr
	}

	size := int64(len(f.getPayload))
	return &s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(f.getPayload)),
		ContentLength: &size,
	}, nil
}

func (f *fakePageCacheS3) PutObject(
	_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options),
) (*s3.PutObjectOutput, error) {
	payload, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}

	f.mutex.Lock()
	f.puts[*input.Key] = payload
	putErr := f.putErr
	f.mutex.Unlock()

	f.putDone <- struct{}{}
	return &s3.PutObjectOutput{}, putErr
}

func (f *fakePageCacheS3) HeadBucket(
	context.Context, *s3.HeadBucketInput, ...func(*s3.Options),
) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

func (f *fakePageCacheS3) storedKeys() map[string][]byte {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	stored := make(map[string][]byte, len(f.puts))
	for key, value := range f.puts {
		stored[key] = value
	}
	return stored
}

func newTestPageCache(t *testing.T, client pageCacheS3API, prefix string) *S3PageCache {
	t.Helper()

	cache := newS3PageCache(client, PageCacheConfig{Bucket: "page-cache", Prefix: prefix, Logger: zerolog.Nop()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, cache.Close(ctx))
	})
	return cache
}

func TestS3PageCacheGetHit(t *testing.T) {
	t.Parallel()

	client := newFakePageCacheS3()
	client.getPayload = []byte("PNGDATA")
	cache := newTestPageCache(t, client, "renders")

	body, size, err := cache.Get(context.Background(), "v1/ab/hash.png")
	require.NoError(t, err)
	require.NotNil(t, body)
	defer body.Close()

	payload, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, "PNGDATA", string(payload))
	require.EqualValues(t, len(payload), size)
	require.Equal(t, []string{"renders/v1/ab/hash.png"}, client.getKeys)
}

// TestS3PageCacheGetMiss covers the contract the worker relies on: a missing object is a miss, not an
// error, so it does not get logged as a cache failure on every cold page.
func TestS3PageCacheGetMiss(t *testing.T) {
	t.Parallel()

	tests := []struct {
		message string
		err     error
	}{
		{message: "no such key", err: &types.NoSuchKey{}},
		{message: "not found", err: &types.NotFound{}},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			t.Parallel()

			client := newFakePageCacheS3()
			client.getErr = tt.err
			cache := newTestPageCache(t, client, "")

			body, size, err := cache.Get(context.Background(), "v1/ab/hash.png")
			require.NoError(t, err)
			require.Nil(t, body)
			require.Zero(t, size)
		})
	}
}

func TestS3PageCacheGetError(t *testing.T) {
	t.Parallel()

	client := newFakePageCacheS3()
	client.getErr = errors.New("access denied")
	cache := newTestPageCache(t, client, "")

	body, _, err := cache.Get(context.Background(), "v1/ab/hash.png")
	require.Error(t, err)
	require.Nil(t, body)
}

func TestS3PageCachePutUploadsAsynchronously(t *testing.T) {
	t.Parallel()

	client := newFakePageCacheS3()
	cache := newTestPageCache(t, client, "renders")

	cache.Put("v1/ab/hash.png", []byte("PNGDATA"))

	select {
	case <-client.putDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the page was never uploaded")
	}

	stored := client.storedKeys()
	require.Equal(t, []byte("PNGDATA"), stored["renders/v1/ab/hash.png"])
}

// TestS3PageCacheCloseDrainsQueue proves a rollout does not throw away pages that were already rendered
// and queued.
func TestS3PageCacheCloseDrainsQueue(t *testing.T) {
	t.Parallel()

	client := newFakePageCacheS3()
	client.putDone = make(chan struct{}, pageCacheQueueSize)
	cache := newS3PageCache(client, PageCacheConfig{Bucket: "page-cache", Logger: zerolog.Nop()})

	for i := range 8 {
		cache.Put(string(rune('a'+i))+".png", []byte("PNGDATA"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, cache.Close(ctx))
	require.Len(t, client.storedKeys(), 8)

	// Close is idempotent, and a write after it is dropped rather than panicking on a closed queue.
	require.NoError(t, cache.Close(ctx))
	cache.Put("late.png", []byte("PNGDATA"))
	require.Len(t, client.storedKeys(), 8)
}

// TestS3PageCachePutShedsWhenFull is the memory bound: with the uploads blocked, writes are dropped
// instead of accumulating rendered pages on the heap.
func TestS3PageCachePutShedsWhenFull(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	client := &blockingPageCacheS3{release: release}
	cache := newS3PageCache(client, PageCacheConfig{Bucket: "page-cache", Logger: zerolog.Nop()})
	t.Cleanup(func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, cache.Close(ctx))
	})

	// Enough to fill every worker and the whole queue, with a surplus that has to be shed.
	total := pageCacheQueueSize + pageCacheUploadWorkers + 32
	for range total {
		cache.Put("v1/ab/hash.png", []byte("PNGDATA"))
	}

	require.LessOrEqual(t, client.callCount(), pageCacheQueueSize+pageCacheUploadWorkers)
}

// blockingPageCacheS3 holds every upload open until release is closed, so the queue can be filled.
type blockingPageCacheS3 struct {
	mutex   sync.Mutex
	calls   int
	release chan struct{}
}

func (b *blockingPageCacheS3) GetObject(
	context.Context, *s3.GetObjectInput, ...func(*s3.Options),
) (*s3.GetObjectOutput, error) {
	return nil, &types.NoSuchKey{}
}

func (b *blockingPageCacheS3) PutObject(
	context.Context, *s3.PutObjectInput, ...func(*s3.Options),
) (*s3.PutObjectOutput, error) {
	b.mutex.Lock()
	b.calls++
	b.mutex.Unlock()

	<-b.release
	return &s3.PutObjectOutput{}, nil
}

func (b *blockingPageCacheS3) HeadBucket(
	context.Context, *s3.HeadBucketInput, ...func(*s3.Options),
) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

func (b *blockingPageCacheS3) callCount() int {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.calls
}
