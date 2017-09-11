package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"net/http"
	_ "net/http/pprof"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Nitro/filecache"
	"github.com/Nitro/lazypdf"
	"github.com/Nitro/ringman"
	log "github.com/sirupsen/logrus"
	"github.com/gorilla/handlers"
)

const (
	// ImageMaxWidth is the maximum supported image width
	ImageMaxWidth = 4096
)

var (
	sanitizer     *regexp.Regexp = regexp.MustCompile(`(^\/|^/|(^\./)+|^(\.\.)+|^(\.)+)`)
	shutdownMutex sync.Mutex
)

func imageQualityForRequest(r *http.Request) int {
	imageQuality := 100
	if r.FormValue("quality") != "" {
		quality, err := strconv.ParseInt(r.FormValue("quality"), 10, 32)
		if err != nil {
			log.Warnf("Got a bad 'quality' value: %s", r.FormValue("quality"))
		} else {
			imageQuality = int(quality)
		}
	}

	return imageQuality
}

func sanitizeFilename(filename string) string {
	replaced := sanitizer.ReplaceAll([]byte(filename), []byte{})
	return string(replaced)
}

func imageTypeForRequest(r *http.Request) string {
	imageType := "image/png"
	iType := r.FormValue("imageType")
	if iType != "" {
		switch iType {
		case "image/jpeg":
			imageType = "image/jpeg"
		case "image/png":
			imageType = "image/png"
		default:
			log.Warnf("Got invalid image type request: %s. Sending image/png", iType)
			imageType = "image/png"
		}
	}

	return imageType
}

func widthForRequest(r *http.Request) (int64, error) {
	var width int64
	var err error
	if r.FormValue("width") != "" {
		width, err = strconv.ParseInt(r.FormValue("width"), 10, 32)
		if err != nil || width < 0 || width > ImageMaxWidth {
			return 0, fmt.Errorf("Invalid width! Limit is %d", ImageMaxWidth)
		}
	}

	return width, nil
}

func makeHandler(wrapped func(http.ResponseWriter, *http.Request, *filecache.FileCache, *RasterCache, *ringman.MemberlistRing),
	cache *filecache.FileCache, rasterCache *RasterCache, ring *ringman.MemberlistRing) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {
		wrapped(w, r, cache, rasterCache, ring)
	}
}

func handleClearRasterCache(w http.ResponseWriter, r *http.Request,
	cache *filecache.FileCache, rasterCache *RasterCache, ring *ringman.MemberlistRing) {

	defer r.Body.Close()

	if !ring.Manager.Ping() {
		http.Error(w, "Node is offline", 503)
		return
	}

	rasterCache.Purge()

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "OK"}`))
}

// handleImage is an HTTP handler that responds to requests for pages
func handleImage(w http.ResponseWriter, r *http.Request,
	cache *filecache.FileCache, rasterCache *RasterCache, ring *ringman.MemberlistRing) {

	defer func(startTime time.Time) {
		log.Debugf("Total request time: %s", time.Since(startTime))
	}(time.Now())

	defer r.Body.Close()

	// Let's first parse out some URL args and return errors if
	// we got some bogus stuff.
	page, err := strconv.ParseInt(r.FormValue("page"), 10, 32)
	if err != nil || page < 1 {
		http.Error(w, "Invalid page", 400)
		return
	}

	imageQuality := imageQualityForRequest(r)
	width, err := widthForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
	}
	imageType := imageTypeForRequest(r)

	// Clean up the URL path into a local filename.
	filename := sanitizeFilename(r.URL.Path)
	storagePath := cache.GetFileName(r.URL.Path)

	// Prevent the node from caching any new documents if it has been marked as offline
	if !ring.Manager.Ping() && !cache.Contains(filename) {
		http.Error(w, "Node is offline", 503)
		return
	}

	// Try to get the file from the cache and/or backing store.
	// NOTE: this can block for a long time while we download a file
	// from the backing store.
	if !cache.Fetch(filename) {
		http.NotFound(w, r)
		return
	}

	// Log how long we take to rasterize things
	defer func(startTime time.Time) {
		log.Debugf("Raster time %s for %s page %d", time.Since(startTime), r.URL.Path, page)
	}(time.Now())

	// Get ahold of a rasterizer for this document, either from the cache,
	// or newly constructed by the cache.
	raster, err := rasterCache.GetRasterizer(storagePath)
	if err != nil {
		log.Errorf("Unable to get rasterizer for %s: '%s'", storagePath, err)
		http.Error(w, fmt.Sprintf("Error encountered while processing pdf %s: '%s'", storagePath, err), 500)
		return
	}

	// Actually render the page to a bitmap so we can encode to JPEG/PNG
	image, err := raster.GeneratePage(int(page), int(width))
	if err != nil {
		if lazypdf.IsBadPage(err) {
			http.Error(w, fmt.Sprintf("Page is not part of this pdf: %s", err), 404)
		} else {
			log.Errorf("Error while processing pdf: %s", err)
			http.Error(w, fmt.Sprintf("Error encountered while processing pdf: %s", err), 500)
		}
		return
	}

	w.Header().Set("Content-Type", imageType)
	w.Header().Set("Cache-Control", "max-age=3600")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	if imageType == "image/jpeg" {
		err = jpeg.Encode(w, image, &jpeg.Options{Quality: imageQuality})
	} else {
		err = png.Encode(w, image)
	}

	if err != nil {
		msg := fmt.Sprintf("Error while encoding image as '%s': %s", imageType, err)
		log.Errorf(msg)
		http.Error(w, msg, 500)
	}
}

// Health route for the service
func handleHealth(w http.ResponseWriter, r *http.Request,
	cache *filecache.FileCache, rasterCache *RasterCache, ring *ringman.MemberlistRing) {

	defer r.Body.Close()

	status := "OK"
	if !ring.Manager.Ping() {
		status = "Offline"
	}

	healthData := struct {
		Status          string
		FileCacheSize   int
		RasterCacheSize int
	}{
		Status:          status,
		FileCacheSize:   cache.Cache.Len(),
		RasterCacheSize: rasterCache.rasterizers.Len(),
	}

	data, err := json.MarshalIndent(healthData, "", "  ")
	if err != nil {
		http.Error(w, `{"status": "error", "message":`+err.Error()+`}`, 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if !ring.Manager.Ping() {
		w.WriteHeader(503)
		w.Write(data)
		return
	}

	w.Write(data)
}

// handleShutdown creates an HTTP handler for triggering a soft shutdown
func handleShutdown(w http.ResponseWriter, r *http.Request,
	cache *filecache.FileCache, _ *RasterCache, ring *ringman.MemberlistRing) {

	defer r.Body.Close()

	// Make sure we don't cause undefined behaviour if shutdown gets called
	// multiple times in parallel
	shutdownMutex.Lock()
	defer shutdownMutex.Unlock()

	if !ring.Manager.Ping() {
		http.Error(w, "Node is offline", 503)
		return
	}

	log.Warnf("Shutdown triggered via HTTP")

	ring.Shutdown()
	go cache.Cache.Purge()

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "OK"}`))

}

func serveHttp(config *Config, cache *filecache.FileCache, ring *ringman.MemberlistRing, rasterCache *RasterCache) error {
	http.HandleFunc("/favicon.ico", http.NotFound)
	http.Handle("/hashring/", http.StripPrefix("/hashring", ring.HttpMux()))
	http.HandleFunc("/health", makeHandler(handleHealth, cache, rasterCache, ring))
	http.HandleFunc("/rastercache/purge", makeHandler(handleClearRasterCache, cache, rasterCache, ring))
	http.HandleFunc("/shutdown", makeHandler(handleShutdown, cache, rasterCache, ring))
	http.HandleFunc("/", makeHandler(handleImage, cache, rasterCache, ring))
	err := http.ListenAndServe(
		fmt.Sprintf(":%s", config.Port), handlers.LoggingHandler(os.Stdout, http.DefaultServeMux),
	)

	if err != nil {
		return errors.New("Unable to serve Http: " + err.Error())
	}

	return nil
}
