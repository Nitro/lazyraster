package main

import (
	"time"
	"fmt"
	"sync"

	"github.com/Nitro/lazypdf"
	"github.com/hashicorp/golang-lru"
	log "github.com/sirupsen/logrus"
)

const (
	// DefaultRasterCacheSize is the default number of cahced rasterizers for open documents
	DefaultRasterCacheSize = 20
)

// RasterCache is a simple LRU cache that holds a number of lazypdf.Rasterizer
// entries. These are then cleaned up on eviction from the cache.
type RasterCache struct {
	rasterizers *lru.Cache
	rasterLock  sync.Mutex
}

// NewDefaultRasterCache hands back a cache with the default configuration.
func NewDefaultRasterCache() (*RasterCache, error) {
	return NewRasterCache(DefaultRasterCacheSize)
}

// NewRasterCache creates a new cache of the defined size.
func NewRasterCache(size int) (*RasterCache, error) {
	rasterCache := &RasterCache{}

	cache, err := lru.NewWithEvict(size, rasterCache.onEvicted)
	if err != nil {
		return nil, err
	}

	rasterCache.rasterizers = cache

	return rasterCache, nil
}

// GetRasterizer will either return a cached rasterizer for the filename in
// question, or will create a new one and then cache it.
func (r *RasterCache) GetRasterizer(filename string, rasterBufferSize int) (*lazypdf.Rasterizer, error) {
	var raster *lazypdf.Rasterizer

	r.rasterLock.Lock()
	defer r.rasterLock.Unlock()

	if rawRaster, ok := r.rasterizers.Get(filename); ok {
		raster = rawRaster.(*lazypdf.Rasterizer)
		return raster, nil
	}

	log.Infof("Initializing new rasterizer for %s", filename)
	t0 := time.Now()
	raster = lazypdf.NewRasterizer(filename, rasterBufferSize)
	err := raster.Run()
	log.Infof("NewRasterizer took %s for %s", time.Since(t0), filename)
	if err != nil {
		return nil, fmt.Errorf("Can't run rasterizer for %s: %s", filename, err)
	}

	r.rasterizers.Add(filename, raster)

	// Visualize raster cache for debug purposes
	log.Infof("rasterCache len: %d, keys: %v", r.rasterizers.Len(), r.rasterizers.Keys())

	return raster, nil
}

// Remove checks if a file is present in the cache and then removes it.
func (r *RasterCache) Remove(filename string) {
	if r.rasterizers.Contains(filename) {
		r.rasterizers.Remove(filename)
		log.Infof("Removed %s from raster cache. len: %s, keys: %v", filename, r.rasterizers.Len(), r.rasterizers.Keys())
	}
}

// Purge cleans out everyting in the rasterizer cache. This will trigger onEvicted() for
// each item in the cache.
func (r *RasterCache) Purge() {
	r.rasterizers.Purge()
	log.Infof("Purged raster cache. len: %s, keys: %v", r.rasterizers.Len(), r.rasterizers.Keys())
}

// onEvicted is the callback that is used when something is removed from the cache,
// either explicitly or via algorithmic removal based on the LRU calculation.
func (r *RasterCache) onEvicted(key interface{}, value interface{}) {
	raster, ok := value.(*lazypdf.Rasterizer)
	if !ok || raster == nil {
		log.Warn("Tried to evict a rasterizer that was nil or couldn't be coerced!")
		return
	}

	raster.Stop()
}
