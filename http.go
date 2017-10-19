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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nitro/filecache"
	"github.com/Nitro/lazypdf"
	"github.com/Nitro/ringman"
	"github.com/Nitro/urlsign"
	"github.com/gorilla/handlers"
	log "github.com/sirupsen/logrus"
	"github.com/yvasiyarov/gorelic"
)

const (
	// ImageMaxWidth is the maximum supported image width
	ImageMaxWidth     = 4096
	ImageMaxScale     = 3.0
	SigningBucketSize = 8 * time.Hour
)

var (
	shutdownMutex sync.Mutex
)

// imageQualityForRequest parses out the value for the imageQuality parameter
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

// imageTypeForRequest parses out the value for the imageType parameter
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

// widthForRequest parses out the value for the width parameter
func widthForRequest(r *http.Request) (int64, error) {
	var width uint64
	var err error
	if r.FormValue("width") != "" {
		width, err = strconv.ParseUint(r.FormValue("width"), 10, 32)
		if err != nil {
			return 0, fmt.Errorf("Invalid width!")
		}

		if width > ImageMaxWidth {
			return 0, fmt.Errorf("Invalid width! Limit is %d", ImageMaxWidth)
		}
	}

	return int64(width), nil
}

// scaleForRequest parses outt he value from the scale parameter
func scaleForRequest(r *http.Request) (float64, error) {
	var scale float64
	var err error
	if r.FormValue("scale") != "" {
		scale, err = strconv.ParseFloat(r.FormValue("scale"), 64)
		if err != nil || scale < 0.0 || scale > ImageMaxScale {
			return 0, fmt.Errorf("Invalid scale! Limit is %f", ImageMaxScale)
		}
	}

	return scale, nil
}

// pageForRequest parses out the value of the page parameter
func pageForRequest(r *http.Request) (int64, error) {
	// Let's first parse out some URL args and return errors if
	// we got some bogus stuff.
	page, err := strconv.ParseUint(r.FormValue("page"), 10, 32)
	if err != nil {
		return -1, fmt.Errorf("Invalid page!")
	}

	return int64(page), nil
}

// timestampForRequest parses out the Unix timestamp from the newerThan parameter
func timestampForRequest(r *http.Request) time.Time {
	timestamp, err := strconv.ParseUint(r.FormValue("newerThan"), 10, 32)
	if err != nil {
		return time.Unix(0, 0)
	}

	return time.Unix(int64(timestamp), 0)
}

type RasterHttpServer struct {
	cache       *filecache.FileCache
	rasterCache *RasterCache
	ring        *ringman.MemberlistRing
	urlSecret   string
	agent       *gorelic.Agent
}

// beginTrace is a safe wrapper around the New Relic agent tracer
func (h *RasterHttpServer) beginTrace(name string) *gorelic.Trace {
	if h.agent != nil {
		t := h.agent.Tracer.BeginTrace(name)
		return t
	}

	return nil
}

// endTrace is a safe wrapper around the New Relic agent tracer
func (h *RasterHttpServer) endTrace(t *gorelic.Trace) {
	if h.agent != nil {
		t.EndTrace()
	}
}

// isValidSignature is a wrapper to handle urlsign.IsValidSignature
func (h *RasterHttpServer) isValidSignature(url string, w http.ResponseWriter) bool {
	if len(h.urlSecret) < 1 {
		return true
	}

	if !urlsign.IsValidSignature(h.urlSecret, SigningBucketSize, time.Now().UTC(), url) {
		http.Error(w, "Invalid signature!", 403)
		return false
	}

	return true
}

// Allows us to manually clear out the raster cache
func (h *RasterHttpServer) handleClearRasterCache(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	if !h.ring.Manager.Ping() {
		http.Error(w, "Node is offline", 503)
		return
	}

	h.rasterCache.Purge()

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "OK"}`))
}

// urlToFilename converts the incoming URL path into a cached filename (this is
// the filename on the backing store, not the cached filename locally).
func urlToFilename(url string) string {
	pathParts := strings.Split(strings.TrimLeft(url, "/documents"), "/")
	if len(pathParts) < 1 {
		return ""
	}

	base := 0

	// XXX Temporary! Strip bucket name from URLs
	base++

	return strings.Join(pathParts[base:], "/")
}

// handleImage is an HTTP handler that responds to requests for pages
func (h *RasterHttpServer) handleImage(w http.ResponseWriter, r *http.Request) {

	// Log some debug timing and send to New Relic
	defer func(startTime time.Time) {
		log.Debugf("Total request time: %s", time.Since(startTime))
	}(time.Now())

	t := h.beginTrace("handleImage")
	defer h.endTrace(t)

	defer r.Body.Close()

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET")

	// If we are supposed to use signed URLs, then do it!
	if !h.isValidSignature(r.URL.String(), w) {
		// The error code/message will already have been handled
		return
	}

	// Parse out and handle some HTTP parameters
	page, err := pageForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	imageQuality := imageQualityForRequest(r)
	width, err := widthForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	imageType := imageTypeForRequest(r)
	scale, err := scaleForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	// Clean up the URL path into a local filename.
	filename := urlToFilename(r.URL.Path)
	if filename == "" || filename == "/" {
		http.Error(w, "Invalid filename!", 404)
		return
	}

	storagePath := h.cache.GetFileName(filename)

	// Prevent the node from caching any new documents if it has been marked as offline
	if h.ring != nil && !h.ring.Manager.Ping() && !h.cache.Contains(filename) {
		http.Error(w, "Node is offline", 503)
		return
	}

	// Try to get the file from the cache and/or backing store.
	// NOTE: this can block for a long time while we download a file
	// from the backing store.
	timestamp := timestampForRequest(r)
	if time.Unix(0, 0).Before(timestamp) { // Cache busting mechanism for forced reload
		if !h.cache.FetchNewerThan(filename, timestamp) {
			http.NotFound(w, r)
			return
		}
	} else {
		if !h.cache.Fetch(filename) {
			http.NotFound(w, r)
			return
		}
	}

	// Log how long we take to rasterize things
	defer func(startTime time.Time) {
		log.Debugf("Raster time %s for %s page %d", time.Since(startTime), r.URL.Path, page)
	}(time.Now())
	t2 := h.beginTrace("rasterize")
	defer h.endTrace(t2)

	// Get ahold of a rasterizer for this document, either from the cache,
	// or newly constructed by the cache.
	raster, err := h.rasterCache.GetRasterizer(storagePath)
	if err != nil {
		log.Errorf("Unable to get rasterizer for %s: '%s'", storagePath, err)
		http.Error(w, fmt.Sprintf("Error encountered while processing pdf %s: '%s'", storagePath, err), 500)
		return
	}

	// Actually render the page to a bitmap so we can encode to JPEG/PNG
	image, err := raster.GeneratePage(int(page), int(width), float64(scale))
	if err != nil {
		if lazypdf.IsBadPage(err) {
			http.Error(w, fmt.Sprintf("Page is not part of this pdf: %s", err), 404)
		} else if lazypdf.IsRasterTimeout(err) {
			http.Error(w, fmt.Sprintf("Page rendering timed out: %s", err), 503)
		} else {
			log.Errorf("Error while processing pdf: %s", err)
			http.Error(w, fmt.Sprintf("Error encountered while processing pdf: %s", err), 500)
		}
		return
	}

	w.Header().Set("Content-Type", imageType)
	w.Header().Set("Cache-Control", "max-age=3600")

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
func (h *RasterHttpServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	status := "OK"
	if !h.ring.Manager.Ping() {
		status = "Offline"
	}

	healthData := struct {
		Status          string
		FileCacheSize   int
		RasterCacheSize int
	}{
		Status:          status,
		FileCacheSize:   h.cache.Cache.Len(),
		RasterCacheSize: h.rasterCache.rasterizers.Len(),
	}

	data, err := json.MarshalIndent(healthData, "", "  ")
	if err != nil {
		http.Error(w, `{"status": "error", "message":`+err.Error()+`}`, 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if !h.ring.Manager.Ping() {
		w.WriteHeader(503)
		w.Write(data)
		return
	}

	w.Write(data)
}

// handleShutdown creates an HTTP handler for triggering a soft shutdown
func (h *RasterHttpServer) handleShutdown(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	// Make sure we don't cause undefined behaviour if shutdown gets called
	// multiple times in parallel
	shutdownMutex.Lock()
	defer shutdownMutex.Unlock()

	if !h.ring.Manager.Ping() {
		http.Error(w, "Node is offline", 503)
		return
	}

	log.Warnf("Shutdown triggered via HTTP")

	h.ring.Shutdown()
	go h.cache.Cache.Purge()

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "OK"}`))

}

// configureServer sets up an http.Server with Read and Write timeouts, and
// a sane header byte length.
func configureServer(config *Config, mux http.Handler) *http.Server {
	return &http.Server{
		Addr:           fmt.Sprintf(":%d", config.HttpPort),
		Handler:        mux,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   15 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 KB
	}
}

func serveHttp(config *Config, cache *filecache.FileCache, ring *ringman.MemberlistRing,
	rasterCache *RasterCache, urlSecret string, agent *gorelic.Agent) error {

	// Protect against garbage configuration
	urlSecret = strings.TrimSpace(urlSecret)

	// Simple wrapper to make definitions simpler to read/understand
	handle := func(f func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
		if agent != nil {
			return agent.WrapHTTPHandlerFunc(f)
		} else {
			return f
		}
	}

	if agent != nil {
		log.Info("Configuring New Relic http tracing")
	}

	if len(urlSecret) < 1 {
		log.Warn("No URL signing secret was passed... running in insecure mode!")
	}

	h := &RasterHttpServer{
		cache:       cache,
		ring:        ring,
		rasterCache: rasterCache,
		urlSecret:   urlSecret,
		agent:       agent,
	}

	// We have to wrap this to make LoggingHandler happy
	docHandler := http.NewServeMux()
	docHandler.HandleFunc("/", handle(h.handleImage))

	// ------------------------------------------------------------------------
	// Route definitions
	// ------------------------------------------------------------------------
	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.ico", http.NotFound) // Browsers look for this
	mux.Handle("/hashring/", http.StripPrefix("/hashring", ring.HttpMux()))
	mux.HandleFunc("/health", handle(h.handleHealth))
	mux.HandleFunc("/rastercache/purge", handle(h.handleClearRasterCache))
	mux.HandleFunc("/shutdown", handle(h.handleShutdown))
	mux.Handle("/documents/", handlers.LoggingHandler(os.Stdout, docHandler))
	// ------------------------------------------------------------------------

	server := configureServer(config, mux)
	err := server.ListenAndServe()

	if err != nil {
		return errors.New("Unable to serve Http: " + err.Error())
	}

	return nil
}
