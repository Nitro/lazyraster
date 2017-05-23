package filecache

import (
	"os"
	"path"
	"path/filepath"
	"sync"

	log "github.com/Sirupsen/logrus"
	"github.com/hashicorp/golang-lru"
)

type FileCache struct {
	BaseDir      string
	Cache        *lru.Cache
	Waiting      map[string]chan struct{}
	WaitLock     sync.Mutex
	DownloadFunc func(fname string, localPath string) error
}

// New returns a properly configured cache. Bubbles up errors from the Hashicrorp
// LRU library when something goes wrong there. The configured cache will have a
// noop DownloadFunc, which should be replaced if you want to actually get files
// from somewhere. Or, look at NewS3Cache() which is backed by Amazon S3.
func New(size int, baseDir string) (*FileCache, error) {
	cache, err := lru.NewWithEvict(size, onEvictDelete)
	if err != nil {
		return nil, err
	}

	fCache := &FileCache{
		Cache:        cache,
		BaseDir:      baseDir,
		Waiting:      make(map[string]chan struct{}),
		DownloadFunc: func(fname string, localPath string) error { return nil },
	}

	return fCache, nil
}

// NewS3Cache returns a cache where the DownloadFunc will pull files from a
// specified S3 bucket. Bubbles up errors from the Hashicrorp LRU library when
// something goes wrong there.
func NewS3Cache(size int, baseDir string, s3Bucket string, awsRegion string) (*FileCache, error) {
	fCache, err := New(size, baseDir)
	if err != nil {
		return nil, err
	}

	fCache.DownloadFunc = func(fname string, localPath string) error {
		return S3Download(fname, localPath, s3Bucket, awsRegion)
	}

	return fCache, nil
}

// Fetch will return true if we have the file, or will go download the file and
// return true if we can. It will return false only if it's unable to fetch the
// file from the backing store (S3).
func (c *FileCache) Fetch(filename string) bool {
	// Try a few non-locking
	if c.Contains(filename) {
		return true
	}

	err := c.MaybeDownload(filename)
	if err != nil {
		log.Errorf("Tried to fetch file %s, got '%s'", filename, err)
		return false
	}

	return true
}

// Contains looks to see if we have an entry in the cache for this filename.
func (c *FileCache) Contains(filename string) bool {
	return c.Cache.Contains(filename)
}

// MaybeDownload might go out to the backing store (S3) and get the file if the
// file isn't already being downloaded in another routine. In both cases it will
// block until the download is completed either by this goroutine or another one.
func (c *FileCache) MaybeDownload(filename string) error {
	// See if someone is already downloading
	c.WaitLock.Lock()
	if waitChan, ok := c.Waiting[filename]; ok {
		c.WaitLock.Unlock()

		log.Debugf("Awaiting download of %s", filename)
		<-waitChan
		return nil
	}

	// The file could have arrived while we were getting here
	if c.Contains(filename) {
		c.WaitLock.Unlock()
		return nil
	}

	// Still don't have it, let's fetch it.
	// This tells other goroutines that we're fetching, and
	// lets us signal completion.
	log.Debugf("Making channel for %s", filename)
	c.Waiting[filename] = make(chan struct{})
	c.WaitLock.Unlock()

	storagePath := c.GetFileName(filename)
	err := c.DownloadFunc(filename, storagePath)
	if err != nil {
		return err
	}

	c.Cache.Add(filename, storagePath)

	c.WaitLock.Lock()
	log.Debugf("Deleting channel for %s", filename)
	close(c.Waiting[filename])  // Notify anyone waiting on us
	delete(c.Waiting, filename) // Remove it from the waiting map
	c.WaitLock.Unlock()

	return nil
}

// GetFileName returns the full storage path and file name for a file, if it were
// in the cache. This does _not_ check to see if the file is actually _in_ the
// cache.
func (c *FileCache) GetFileName(filename string) string {
	dir, file := filepath.Split(filename)
	return filepath.Join(c.BaseDir, dir, filepath.FromSlash(path.Clean("/"+file)))
}

// onEvictDelete is a callback that is triggered when the LRU cache expires an
// entry.
func onEvictDelete(key interface{}, value interface{}) {
	filename := key.(string)
	storagePath := value.(string)

	log.Debugf("Got eviction notice for '%s', removing", key)

	err := os.Remove(storagePath)
	if err != nil {
		log.Errorf("Unable to evict '%s' at local path '%s': %s", filename, storagePath, err)
		return
	}
}
