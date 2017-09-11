package main

import (
	"fmt"
	"sync"

	"github.com/Nitro/lazypdf"
	log "github.com/sirupsen/logrus"
	"github.com/hashicorp/golang-lru"
)

// #cgo CFLAGS: -I../lazypdf -I../lazypdf/mupdf-1.11-source/include -I../lazypdf/mupdf-1.11-source/include/mupdf -I../lazypdf/mupdf-1.11-source/thirdparty/openjpeg -I../lazypdf/mupdf-1.11-source/thirdparty/jbig2dec -I../lazypdf/mupdf-1.11-source/thirdparty/zlib -I../lazypdf/mupdf-1.11-source/thirdparty/jpeg -I../lazypdf/mupdf-1.11-source/thirdparty/freetype
// #cgo LDFLAGS: -L../lazypdf/mupdf-1.11-source/build/release -lmupdf -lmupdfthird -lm -ljbig2dec -lz -lfreetype -ljpeg -lcrypto -lpthread
// #include <faster_raster.h>
import "C"

const (
	// DefaultRasterCacheSize is the default number of cahced rasterizers for open documents
	DefaultRasterCacheSize = 20
)

// RasterCache is a simple LRU cache that holds a number of lazypdf.Rasterizer
// entries. These are then cleaned up on eviction from the cache.
type RasterCache struct {
	rasterizers         *lru.Cache
	rasterLock          sync.Mutex
	mostRecentlyStopped *lazypdf.Rasterizer
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
func (r *RasterCache) GetRasterizer(filename string) (*lazypdf.Rasterizer, error) {
	var raster *lazypdf.Rasterizer
	r.rasterLock.Lock()
	defer r.rasterLock.Unlock()

	if rawRaster, ok := r.rasterizers.Get(filename); ok {
		raster = rawRaster.(*lazypdf.Rasterizer)
		return raster, nil
	}

	log.Debugf("Initializing new rasterizer for %s", filename)

	raster = lazypdf.NewRasterizer(filename)
	err := raster.Run()
	if err != nil {
		return nil, fmt.Errorf("Can't run rasterizer for %s: %s", filename, err)
	}
	r.rasterizers.Add(filename, raster)

	return raster, nil
}

// Remove checks if a file is present in the cache and then removes it.
func (r *RasterCache) Remove(filename string) {
	if r.rasterizers.Contains(filename) {
		r.rasterizers.Remove(filename)
	}
}

// Purge cleans out everyting in the rasterizer cache. This will trigger onEvicted() for
// each item in the cache.
func (r *RasterCache) Purge() {
	r.rasterizers.Purge()
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
	r.mostRecentlyStopped = raster
}
