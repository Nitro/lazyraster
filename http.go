package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nitro/filecache"
	"github.com/Nitro/lazypdf"
	"github.com/Nitro/urlsign"
	"github.com/gorilla/handlers"
	log "github.com/sirupsen/logrus"
	ddHTTP "gopkg.in/DataDog/dd-trace-go.v1/contrib/net/http"
	ddTracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

const (
	// ImageMaxWidth is the maximum supported image width
	ImageMaxWidth      = 4096
	ImageMaxScale      = 3.0
	SigningBucketSize  = 8 * time.Hour
	ServerReadTimeout  = 10 * time.Second
	ServerWriteTimeout = 15 * time.Second
)

var (
	shutdownMutex  sync.Mutex
	gzipWriterPool sync.Pool
)

// RasterDocumentParams stores all the parameters from the web request that
// are needed to fetch a document.
type RasterDocumentParams struct {
	Timestamp      time.Time
	DownloadRecord *filecache.DownloadRecord
	StoragePath    string
}

// RasterImageParams stores all the parameters from the web request that
// are required for the raster operation.
type RasterImageParams struct {
	Page         int
	Width        int
	Scale        float64
	ImageType    string
	ImageQuality int
}

// DocumentMetadata contains information about the requested document
type DocumentMetadata struct {
	Filename  string
	PageCount int
}

// FilecacheEntry contains a Filecache entry
type FilecacheEntry struct {
	Path           string
	StoragePath    string
	LoadedInMemory bool
}

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
		case "image/svg+xml":
			imageType = "image/svg+xml"
		default:
			log.Warnf("Got invalid image type request: %s. Sending image/png", iType)
			imageType = "image/png"
		}
	}

	return imageType
}

// widthForRequest parses out the value for the width parameter
func widthForRequest(r *http.Request) (int, error) {
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

	return int(width), nil
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
func pageForRequest(r *http.Request) (int, error) {
	// Let's first parse out some URL args and return errors if
	// we got some bogus stuff.
	page, err := strconv.ParseUint(r.FormValue("page"), 10, 32)
	if err != nil {
		return -1, fmt.Errorf("Invalid page!")
	}

	return int(page), nil
}

// timestampForRequest parses out the Unix timestamp from the newerThan parameter
func timestampForRequest(r *http.Request) time.Time {
	timestamp, err := strconv.ParseUint(r.FormValue("newerThan"), 10, 32)
	if err != nil {
		return time.Unix(0, 0)
	}

	return time.Unix(int64(timestamp), 0)
}

type Clock interface {
	Now() time.Time
}

type utcClock struct {
}

func (c *utcClock) Now() time.Time {
	return time.Now().UTC()
}

type RasterHttpServer struct {
	cache            *filecache.FileCache
	rasterCache      *RasterCache
	urlSecret        string
	clock            Clock
	rasterBufferSize int
}

// beginTrace is a safe wrapper around the Datadog agent tracer
func (h *RasterHttpServer) beginTrace(ctx context.Context, name string) ddTracer.Span {
	span, _ := ddTracer.StartSpanFromContext(ctx, name)
	return span
}

// endTrace is a safe wrapper around the Datadog agent tracer
func (h *RasterHttpServer) endTrace(span ddTracer.Span) {
	span.Finish()
}

// isValidSignature is a wrapper to handle urlsign.IsValidSignature
func (h *RasterHttpServer) isValidSignature(url string, w http.ResponseWriter) bool {
	if len(h.urlSecret) < 1 {
		return true
	}

	if !urlsign.IsValidSignature(h.urlSecret, SigningBucketSize, h.clock.Now(), url) {
		http.Error(w, "Invalid signature!", 403)
		return false
	}

	return true
}

// handleListFilecache lists the contents of the disk cache along with the in memory status of each entry
func (h *RasterHttpServer) handleListFilecache(w http.ResponseWriter, _ *http.Request) {
	payload := make([]FilecacheEntry, 0, h.cache.Cache.Len())
	for _, key := range h.cache.Cache.Keys() {
		if storagePath, ok := h.cache.Cache.Get(key); ok {
			_, loadedInMemory := h.rasterCache.rasterizers.Get(storagePath)
			payload = append(payload,
				FilecacheEntry{
					Path:           key.(string),
					StoragePath:    storagePath.(string),
					LoadedInMemory: loadedInMemory,
				},
			)
		}
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		http.Error(w, `{"status": "error", "message":`+err.Error()+`}`, 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(data)
	if err != nil {
		log.Errorf("failed to send payload to client: %s", err)
	}
}

// handleClearRasterCache allows us to manually clear out the raster cache
func (h *RasterHttpServer) handleClearRasterCache(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	h.rasterCache.Purge()

	w.Header().Set("Content-Type", "application/json")
	_, err := io.WriteString(w, `{"status": "OK"}`)
	if err != nil {
		log.Errorf("failed to send payload to client: %s", err)
	}
}

func (h *RasterHttpServer) processDocumentParams(r *http.Request) (*RasterDocumentParams, int, error) {
	var docParams RasterDocumentParams

	var err error
	docParams.DownloadRecord, err = filecache.NewDownloadRecord(r.URL.Path, nil)
	if err != nil {
		return nil, 404, errors.New("Invalid URL path")
	}

	docParams.StoragePath = h.cache.GetFileName(docParams.DownloadRecord)
	docParams.Timestamp = timestampForRequest(r)

	return &docParams, 0, nil

}

func (h *RasterHttpServer) processImageParams(r *http.Request) (*RasterImageParams, int, error) {
	var imgParams RasterImageParams
	var err error

	// Parse out and handle some HTTP parameters
	imgParams.Page, err = pageForRequest(r)
	if err != nil {
		return nil, 400, err
	}

	imgParams.ImageQuality = imageQualityForRequest(r)

	imgParams.Width, err = widthForRequest(r)
	if err != nil {
		return nil, 400, err
	}

	imgParams.ImageType = imageTypeForRequest(r)

	imgParams.Scale, err = scaleForRequest(r)
	if err != nil {
		return nil, 400, err
	}

	return &imgParams, 0, nil
}

// handleCORS is a wrapper which sets the appropriate CORS headers before invoking the
// specified HandlerFunc
func handleCORS(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")

		// For OPTIONS requests, we just forward the Access-Control-Request-Headers as
		// Access-Control-Allow-Headers in the reply and return
		if r.Method == http.MethodOptions {
			if headers, ok := r.Header["Access-Control-Request-Headers"]; ok {
				for _, header := range headers {
					w.Header().Add("Access-Control-Allow-Headers", header)
				}
			}

			return
		}

		handler(w, r)
	}
}

// handleDocument is an HTTP handler that responds to requests for documents
func (h *RasterHttpServer) handleDocument(w http.ResponseWriter, r *http.Request) {

	// Log some debug timing and send to Datadog
	defer func(startTime time.Time) {
		log.Debugf("Total request time: %s", time.Since(startTime))
	}(time.Now())

	t := h.beginTrace(r.Context(), "handleDocument")
	defer h.endTrace(t)

	defer r.Body.Close()

	// If we are supposed to use signed URLs, then do it!
	if !h.isValidSignature(r.URL.String(), w) {
		// The error code/message will already have been handled
		return
	}

	// Process all the incoming parameters into a RasterParams struct
	docParams, status, err := h.processDocumentParams(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	// Get notified when the socket closes
	socketClosed := false
	handlerDone := make(chan struct{})
	defer func() { close(handlerDone) }()
	go func() {
		select {
		// Wait for an early abort from the client while the handler is still running
		case <-r.Context().Done():
			socketClosed = true
		// Make sure this goroutine doesn't block forever if r.Context() somehow doesn't
		// get cancelled when the connection is closed abruptly or when the ServeHTTP
		// method returns
		case <-handlerDone:
		}
	}()

	// Try to get the file from the cache and/or backing store.
	// NOTE: this can block for a long time while we download a file
	// from the backing store.
	if time.Unix(0, 0).Before(docParams.Timestamp) { // Cache busting mechanism for forced reload
		if !h.cache.FetchNewerThan(docParams.DownloadRecord, docParams.Timestamp) {
			http.NotFound(w, r)
			return
		}
	} else {
		if !h.cache.Fetch(docParams.DownloadRecord) {
			http.NotFound(w, r)
			return
		}
	}

	// Log how long we take to rasterize things
	defer func(startTime time.Time) {
		log.Infof("Raster time %s for %s", time.Since(startTime), r.URL.Path)
	}(time.Now())
	t2 := h.beginTrace(r.Context(), "rasterize")
	defer h.endTrace(t2)

	// Get ahold of a rasterizer for this document either from the cache
	// or newly constructed by the cache.
	raster, err := h.rasterCache.GetRasterizer(docParams.StoragePath, h.rasterBufferSize)
	if err != nil {
		log.Errorf("Unable to get rasterizer for %s: %s", docParams.StoragePath, err)
		http.Error(w, "Error encountered while processing pdf", 500)
		return
	}

	if socketClosed {
		log.Infof("Socket closed by client, aborting request for %q", r.URL.Path)
		return
	}

	// if page is not included in request params, we return a JSON payload with
	// document metadata
	if r.FormValue("page") == "" {
		h.handleDocumentInfo(w, docParams, raster)
		return

	}

	h.handleImage(w, r, raster, &socketClosed)
}

func (h *RasterHttpServer) handleDocumentInfo(w http.ResponseWriter, docParams *RasterDocumentParams, raster *lazypdf.Rasterizer) {
	payload := DocumentMetadata{
		Filename:  docParams.DownloadRecord.Path,
		PageCount: raster.GetPageCount(),
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		http.Error(w, `{"status": "error", "message":`+err.Error()+`}`, 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(data)
	if err != nil {
		log.Errorf("failed to send payload to client: %s", err)
	}
}

func writeImage(w http.ResponseWriter, image image.Image, imgParams *RasterImageParams) error {
	if imgParams.ImageType == "image/jpeg" {
		return jpeg.Encode(w, image, &jpeg.Options{Quality: imgParams.ImageQuality})
	}

	return png.Encode(w, image)
}

// supportsGzipEncoding makes sure that we don't have any false positives
// Inspired from https://groups.google.com/d/msg/golang-nuts/NVnqAzKbrKQ/6S9wR_zdg4EJ
func supportsGzipEncoding(req *http.Request) bool {
	for _, v := range strings.Split(strings.ToLower(req.Header.Get("Accept-Encoding")), ",") {
		if strings.ToLower(strings.TrimSpace(v)) == "gzip" {
			return true
		}
	}
	return false
}

// acquireGzipWriter tries to return a cached gzip.Writer. It will create a new one
// if none are available.
func acquireGzipWriter(w http.ResponseWriter) *gzip.Writer {
	cachedObject := gzipWriterPool.Get()
	if cachedObject == nil {
		return gzip.NewWriter(w)
	}

	gzw := cachedObject.(*gzip.Writer)
	gzw.Reset(w)

	return gzw
}

// releaseGzipWriter returns a closed gzip.Writer to the gzip writer pool for reuse
func releaseGzipWriter(gzw *gzip.Writer) error {
	err := gzw.Close()

	// I think it might be a bad idea to cache writers which errored when
	// we tried to close them.
	if err != nil {
		return err
	}

	gzipWriterPool.Put(gzw)

	return nil
}

// writeSVG writes the SVG data to the HTTP response
// Note: err is a named return value because we want to update it in a defer
// statement below, when releasing the gzip.Writer.
func writeSVG(w http.ResponseWriter, r *http.Request, svg []byte) (err error) {
	if supportsGzipEncoding(r) {
		gzw := acquireGzipWriter(w)

		w.Header().Add("Content-Encoding", "gzip")
		// Allow intermediary services to cache different encodings for the same request.
		w.Header().Add("Vary", "Accept-Encoding")

		// Closing the writer can sometimes return an error. We want to return this
		// error if we don't already have an error from another place in this function.
		defer func() {
			errClose := releaseGzipWriter(gzw)
			if err == nil && errClose != nil {
				err = fmt.Errorf("failed to release gzip writer: %s", errClose)
			}
		}()

		_, err = gzw.Write(svg)
	} else {
		_, err = w.Write(svg)
	}

	if err != nil {
		return fmt.Errorf("failed to write SVG to response: %s", err)
	}

	return
}

// handleImage is an HTTP handler that responds to requests for pages
func (h *RasterHttpServer) handleImage(w http.ResponseWriter, r *http.Request, raster *lazypdf.Rasterizer, socketClosed *bool) {
	t := h.beginTrace(r.Context(), "handleImage")
	defer h.endTrace(t)

	imgParams, status, err := h.processImageParams(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	var responseWriterFunc func() error
	if imgParams.ImageType == "image/svg+xml" {
		var svg []byte
		svg, err = raster.GeneratePageSVG(r.Context(), imgParams.Page, imgParams.Width, imgParams.Scale)
		responseWriterFunc = func() error { return writeSVG(w, r, svg) }
	} else {
		// Actually render the page to a bitmap so we can encode to JPEG/PNG
		var image image.Image
		image, err = raster.GeneratePageImage(r.Context(), imgParams.Page, imgParams.Width, imgParams.Scale)
		responseWriterFunc = func() error { return writeImage(w, image, imgParams) }
	}
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

	if *socketClosed {
		log.Infof("Socket closed by client, aborting request for %q", r.URL.Path)
		return
	}

	w.Header().Set("Content-Type", imgParams.ImageType)
	w.Header().Set("Cache-Control", fmt.Sprintf("max-age=%d", int64(SigningBucketSize)/1e9))

	err = responseWriterFunc()
	if err != nil && !strings.Contains(err.Error(), "write: broken pipe") {
		msg := fmt.Sprintf("Error while encoding image as %q: %s", imgParams.ImageType, err)
		log.Errorf(msg)
		http.Error(w, msg, 500)
	}
}

// Health route for the service
func (h *RasterHttpServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	status := "OK"

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

	_, err = w.Write(data)
	if err != nil {
		log.Errorf("failed to send payload to client: %s", err)
	}
}

// handleShutdown creates an HTTP handler for triggering a soft shutdown
func (h *RasterHttpServer) handleShutdown(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	// Make sure we don't cause undefined behaviour if shutdown gets called
	// multiple times in parallel
	shutdownMutex.Lock()
	defer shutdownMutex.Unlock()

	log.Warnf("Shutdown triggered via HTTP")

	go h.cache.Cache.Purge()

	w.Header().Set("Content-Type", "application/json")
	_, err := io.WriteString(w, `{"status": "OK"}`)
	if err != nil {
		log.Errorf("failed to send payload to client: %s", err)
	}
}

// configureServer sets up an http.Server with Read and Write timeouts, and
// a sane header byte length.
func configureServer(config *Config, mux http.Handler) *http.Server {
	return &http.Server{
		Addr:           fmt.Sprintf(":%d", config.HttpPort),
		Handler:        mux,
		ReadTimeout:    ServerReadTimeout,
		WriteTimeout:   ServerWriteTimeout,
		MaxHeaderBytes: 1 << 20, // 1 KB
	}
}

func serveHttp(config *Config, cache *filecache.FileCache,
	rasterCache *RasterCache, urlSecret string) error {

	// Protect against garbage configuration
	urlSecret = strings.TrimSpace(urlSecret)

	if len(urlSecret) < 1 {
		log.Warn("No URL signing secret was passed... Running in insecure mode!")
	}

	h := &RasterHttpServer{
		cache:            cache,
		rasterCache:      rasterCache,
		urlSecret:        urlSecret,
		clock:            &utcClock{},
		rasterBufferSize: config.RasterBufferSize,
	}

	// We have to wrap this to make LoggingHandler happy
	handler := h.handleDocument
	if !config.DisableCORS {
		handler = handleCORS(handler)
	}
	docHandler := http.NewServeMux()
	docHandler.HandleFunc("/", handler)

	// ------------------------------------------------------------------------
	// Route definitions
	// ------------------------------------------------------------------------
	mux := ddHTTP.NewServeMux()
	mux.HandleFunc("/favicon.ico", http.NotFound) // Browsers look for this
	mux.HandleFunc("/health", h.handleHealth)
	mux.HandleFunc("/filecache/list", h.handleListFilecache)
	mux.HandleFunc("/rastercache/purge", h.handleClearRasterCache)
	mux.HandleFunc("/shutdown", h.handleShutdown)
	mux.Handle("/documents/", handlers.LoggingHandler(os.Stdout, docHandler))
	// ------------------------------------------------------------------------

	server := configureServer(config, mux)

	err := server.ListenAndServe()
	if err != nil {
		return fmt.Errorf("Unable to serve Http: %s", err)
	}

	return nil
}
