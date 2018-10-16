package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nitro/filecache"
	. "github.com/smartystreets/goconvey/convey"
)

// CopyFile copies the contents from src to dst using io.Copy.
// If dst does not exist, CopyFile creates it with permissions perm;
// otherwise CopyFile truncates it before writing.
func CopyFile(dst, src string, perm os.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return
	}
	defer func() {
		if e := out.Close(); e != nil {
			err = e
		}
	}()
	_, err = io.Copy(out, in)
	return
}

func CopyFileToWriter(dst io.Writer, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	_, err = io.Copy(dst, in)
	if err != nil {
		return err
	}

	return nil
}

type CustomResponseRecorder struct {
	httptest.ResponseRecorder
	numSuccessfulWrites int
	totalAllowedWrites  int
}

func (rw *CustomResponseRecorder) Write(buf []byte) (int, error) {
	if rw.numSuccessfulWrites >= rw.totalAllowedWrites {
		return 0, errors.New("write error")
	}

	rw.numSuccessfulWrites += 1

	return rw.ResponseRecorder.Write(buf)
}

func Test_EndToEnd(t *testing.T) {
	Convey("End-to-end testing handleDocument()", t, func() {
		didDownload := false
		downloadCount := 0
		downloadShouldSleep := false
		downloadShouldError := false
		var countLock sync.Mutex

		mockDownloader := func(dr *filecache.DownloadRecord, localPath string) error {
			if downloadShouldError {
				return errors.New("Oh no! Tragedy!")
			}
			if downloadShouldSleep {
				time.Sleep(10 * time.Millisecond)
			}
			countLock.Lock()
			downloadCount += 1
			countLock.Unlock()
			didDownload = true
			return nil
		}

		cache, _ := filecache.New(10, os.TempDir(), filecache.DownloadTimeout(1*time.Millisecond),
			filecache.DefaultExtension(".pdf"),
			filecache.S3Downloader("gondor-north-1"),
		)
		cache.DownloadFunc = mockDownloader

		rasterCache, _ := NewRasterCache(1)

		h := &RasterHttpServer{
			cache:       cache,
			ring:        nil, // Don't test with ringman for now
			rasterCache: rasterCache,
			urlSecret:   "",
			agent:       nil,
		}

		dr, _ := filecache.NewDownloadRecord("/documents/somewhere/sample.pdf", nil)
		So(cache.GetFileName(dr), ShouldEndWith, "12/c3e2cc0a00a4f64dfce9da6647d9ad84.pdf")
		drNoExtension, _ := filecache.NewDownloadRecord("/documents/somewhere/sample", nil)
		So(cache.GetFileName(drNoExtension), ShouldEndWith, "4d/6090c594d41728a7d7ad1e1a4d58cd28.pdf")

		Reset(func() {
			os.Remove(cache.GetFileName(dr))
			os.Remove(cache.GetFileName(drNoExtension))
		})

		Convey("Handling error conditions", func() {
			Convey("When the document is not written properly to disk", func() {
				// Fetch a file which doesn't exist, but leave downloadShouldError = false
				// so mockDownloader doesn't return an error.
				req := httptest.NewRequest("GET", "/documents/somewhere/asdf.pdf", nil)
				recorder := httptest.NewRecorder()

				h.handleDocument(recorder, req)
				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(recorder.Result().StatusCode, ShouldEqual, 500)
				So(string(body), ShouldContainSubstring, "Error encountered while processing pdf")
			})

			Convey("When the page is not contained in the document", func() {
				err := os.MkdirAll(filepath.Dir(cache.GetFileName(dr)), 0755)
				So(err, ShouldBeNil)
				err = CopyFile(cache.GetFileName(dr), "fixtures/sample.pdf", 0644)
				So(err, ShouldBeNil)

				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=10", nil)
				recorder := httptest.NewRecorder()

				h.handleDocument(recorder, req)
				So(recorder.Result().StatusCode, ShouldEqual, 404)
			})

			Convey("When the page is not valid", func() {
				err := os.MkdirAll(filepath.Dir(cache.GetFileName(dr)), 0755)
				So(err, ShouldBeNil)
				err = CopyFile(cache.GetFileName(dr), "fixtures/sample.pdf", 0644)
				So(err, ShouldBeNil)

				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=-1", nil)
				recorder := httptest.NewRecorder()

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(recorder.Result().StatusCode, ShouldEqual, 400)
				So(string(body), ShouldContainSubstring, "Invalid page")
			})

			Convey("When file is not present", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/asdf.pdf", nil)
				recorder := httptest.NewRecorder()

				downloadShouldError = true
				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(recorder.Result().StatusCode, ShouldEqual, 404)
				So(string(body), ShouldContainSubstring, "page not found")
			})

			Convey("Rejects badly/unsigned URLs when signing is required", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1", nil)
				recorder := httptest.NewRecorder()
				h.urlSecret = "secret"

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(recorder.Result().StatusCode, ShouldEqual, 403)
				So(string(body), ShouldContainSubstring, "Invalid signature")
			})

			Convey("Doesn't accept negative width", func() {
				err := os.MkdirAll(filepath.Dir(cache.GetFileName(dr)), 0755)
				So(err, ShouldBeNil)
				err = CopyFile(cache.GetFileName(dr), "fixtures/sample.pdf", 0644)
				So(err, ShouldBeNil)

				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1&width=-300", nil)
				recorder := httptest.NewRecorder()

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(recorder.Result().StatusCode, ShouldEqual, 400)
				So(string(body), ShouldContainSubstring, "Invalid width")
			})

			Convey("Doesn't accept crazy wide width", func() {
				err := os.MkdirAll(filepath.Dir(cache.GetFileName(dr)), 0755)
				So(err, ShouldBeNil)
				err = CopyFile(cache.GetFileName(dr), "fixtures/sample.pdf", 0644)
				So(err, ShouldBeNil)

				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1&width=300000", nil)
				recorder := httptest.NewRecorder()

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(recorder.Result().StatusCode, ShouldEqual, 400)
				So(string(body), ShouldContainSubstring, "Invalid width")
			})

			Convey("Doesn't accept URL paths without a bucket", func() {
				req := httptest.NewRequest("GET", "/documents/sample.pdf?page=1", nil)
				recorder := httptest.NewRecorder()

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(recorder.Result().StatusCode, ShouldEqual, 404)
				So(string(body), ShouldContainSubstring, "Invalid URL path")
			})
		})

		Convey("When everything is working", func() {
			err := os.MkdirAll(filepath.Dir(cache.GetFileName(dr)), 0755)
			So(err, ShouldBeNil)
			err = os.MkdirAll(filepath.Dir(cache.GetFileName(drNoExtension)), 0755)
			So(err, ShouldBeNil)
			err = CopyFile(cache.GetFileName(dr), "fixtures/sample.pdf", 0644)
			So(err, ShouldBeNil)
			err = CopyFile(cache.GetFileName(drNoExtension), "fixtures/sample.pdf", 0644)
			So(err, ShouldBeNil)

			recorder := httptest.NewRecorder()

			Convey("Handles a normal request", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1", nil)

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(len(body), ShouldBeGreaterThan, 1024)
				So(recorder.Result().StatusCode, ShouldEqual, 200)
				So(didDownload, ShouldBeTrue)
				So(downloadCount, ShouldEqual, 1)
			})

			Convey("Handles a jpeg", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1&width=1024&quality=75&imageType=image/jpeg", nil)

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(len(body), ShouldBeGreaterThan, 1024)
				So(recorder.Result().StatusCode, ShouldEqual, 200)
				So(recorder.Result().Header["Content-Type"][0], ShouldEqual, "image/jpeg")
			})

			Convey("Handles a png", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1&width=1024&quality=75&imageType=image/png", nil)

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(len(body), ShouldBeGreaterThan, 1024)
				So(recorder.Result().StatusCode, ShouldEqual, 200)
				So(recorder.Result().Header["Content-Type"][0], ShouldEqual, "image/png")
			})

			Convey("Handles a svg", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1&width=1024&quality=75&imageType=image/svg%2Bxml", nil)

				h.handleDocument(recorder, req)
				So(recorder.Result().StatusCode, ShouldEqual, 200)
				So(recorder.Result().Header.Get("Content-Type"), ShouldEqual, "image/svg+xml")
				So(recorder.Result().Header.Get("Content-Encoding"), ShouldBeBlank)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(string(body), ShouldStartWith, `<?xml version="1.0" encoding="UTF-8" standalone="no"?>`)
				So(string(body), ShouldContainSubstring, "</clipPath>")
				So(string(body), ShouldEndWith, "</svg>\n")
			})

			Convey("Handles a svg with gzip compression", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1&width=1024&quality=75&imageType=image/svg%2Bxml", nil)
				req.Header.Add("Accept-Encoding", "gzip")

				h.handleDocument(recorder, req)
				So(recorder.Result().StatusCode, ShouldEqual, 200)
				So(recorder.Result().Header.Get("Content-Type"), ShouldEqual, "image/svg+xml")
				So(recorder.Result().Header.Get("Content-Encoding"), ShouldEqual, "gzip")
				So(recorder.Result().Header.Get("Vary"), ShouldEqual, "Accept-Encoding")

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(len(body), ShouldBeGreaterThan, 1024)
				So(len(body), ShouldBeLessThan, 131072)
			})

			Convey("Returns the expected errors when writing gzip data", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1&width=1024&quality=75&imageType=image/svg%2Bxml", nil)
				req.Header.Add("Accept-Encoding", "gzip")

				recorder := &CustomResponseRecorder{
					ResponseRecorder: httptest.ResponseRecorder{
						HeaderMap: make(http.Header),
						Body:      new(bytes.Buffer),
						Code:      200,
					},
				}

				Convey("and the svg gzip writer finishes processing successfully", func() {
					// Go will call recorder.Write() 4 times
					recorder.totalAllowedWrites = 4
					err := writeSVG(recorder, req, []byte{})

					So(err, ShouldBeNil)
				})

				Convey("and the svg gzip writer can't write", func() {
					recorder.totalAllowedWrites = 0
					err := writeSVG(recorder, req, []byte{})

					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldContainSubstring, "failed to write SVG to response: write error")
				})

				Convey("and the svg gzip writer can't be closed", func() {
					recorder.totalAllowedWrites = 1
					err := writeSVG(recorder, req, []byte{})

					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldContainSubstring, "failed to release gzip writer: write error")
				})
			})

			Convey("Handles a bunch of options", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1&scale=1.5&quality=75", nil)

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(len(body), ShouldBeGreaterThan, 1024)
				So(recorder.Result().StatusCode, ShouldEqual, 200)
			})

			Convey("Handles a file with no file extension", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample?page=1&scale=1.5&quality=75", nil)

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(len(body), ShouldBeGreaterThan, 1024) // We really did get an image
				So(recorder.Result().StatusCode, ShouldEqual, 200)
			})

			Convey("Returns document metadata when no page number is specified", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf", nil)

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(recorder.Result().StatusCode, ShouldEqual, 200)

				var meta DocumentMetadata
				err = json.Unmarshal(body, &meta)
				So(err, ShouldBeNil)
				So(meta.Filename, ShouldEqual, "somewhere/sample.pdf")
				So(meta.PageCount, ShouldEqual, 2)
			})

			Convey("Sets the appropriate CORS headers", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf", nil)

				dummyHandlerFunc := func(w http.ResponseWriter, r *http.Request) {}

				handleCORS(dummyHandlerFunc)(recorder, req)

				So(recorder.Header(), ShouldContainKey, "Access-Control-Allow-Origin")
				So(recorder.Header()["Access-Control-Allow-Origin"], ShouldContain, "*")
				So(recorder.Header(), ShouldContainKey, "Access-Control-Allow-Methods")
				So(recorder.Header()["Access-Control-Allow-Methods"], ShouldContain, "GET, OPTIONS")
			})

			Convey("Returns the Access Control Headers in the response for OPTIONS", func() {
				req := httptest.NewRequest("OPTIONS", "/documents/somewhere/sample.pdf?page=1", nil)
				req.Header.Add("Access-Control-Request-Headers", "dropbox_token")
				req.Header.Add("Access-Control-Request-Headers", "google_token")

				dummyHandlerFunc := func(w http.ResponseWriter, r *http.Request) {}

				handleCORS(dummyHandlerFunc)(recorder, req)

				So(recorder.Header(), ShouldContainKey, "Access-Control-Allow-Headers")
				So(recorder.Header()["Access-Control-Allow-Headers"], ShouldResemble, []string{"dropbox_token", "google_token"})
			})
		})

		Convey("When timestamps are supplied for cache busting", func() {
			filename := cache.GetFileName(dr)
			err := os.MkdirAll(filepath.Dir(filename), 0755)
			So(err, ShouldBeNil)
			err = CopyFile(filename, "fixtures/sample.pdf", 0644)
			So(err, ShouldBeNil)
			recorder := httptest.NewRecorder()

			cache.Cache.Add("somewhere/sample.pdf", filename)
			// On reload the file gets evicted/deleted so we need to put it back
			reloadableDownloader := func(dr *filecache.DownloadRecord, localPath string) error {
				errCopy := CopyFile(filename, "fixtures/sample.pdf", 0644)
				So(errCopy, ShouldBeNil)

				return mockDownloader(dr, localPath)
			}
			cache.DownloadFunc = reloadableDownloader

			Convey("Downloads if the timestamp is newer", func() {
				fileTime := time.Now().Add(1 * time.Second) // File times are local time!
				req := httptest.NewRequest(
					"GET",
					fmt.Sprintf("/documents/somewhere/sample.pdf?newerThan=%d&page=1", fileTime.Unix()),
					nil,
				)

				h.handleDocument(recorder, req)

				So(didDownload, ShouldBeTrue)
			})

			Convey("Doesn't download if the timestamp is absent", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1", nil)

				h.handleDocument(recorder, req)

				So(didDownload, ShouldBeFalse)
			})

			Convey("Doesn't download if the timestamp is older", func() {
				fileTime := time.Now().Add(-1 * time.Second) // File times are local time!
				req := httptest.NewRequest(
					"GET",
					fmt.Sprintf("/documents/somewhere/sample.pdf?newerThan=%d&page=1", fileTime.Unix()),
					nil,
				)

				h.handleDocument(recorder, req)

				So(didDownload, ShouldBeFalse)

			})
		})

		Convey("Handles a Dropbox request", func() {
			// Set up a real Dropbox cache
			dropboxCache, _ := filecache.New(1, os.TempDir(), filecache.DownloadTimeout(100*time.Millisecond),
				filecache.DefaultExtension(".pdf"),
				filecache.DropboxDownloader(),
			)
			h.cache = dropboxCache

			// Create a mock HTTP server which reads a PDF file and serves it on the default endpoint
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				err := CopyFileToWriter(w, "fixtures/sample.pdf")
				if err != nil {
					http.Error(w, "error reading file", 500)
				}
			}))
			defer ts.Close()

			path := fmt.Sprintf(
				"/documents/dropbox/%s",
				base64.StdEncoding.EncodeToString([]byte(ts.URL)),
			)

			req := httptest.NewRequest("GET", path, nil)

			recorder := httptest.NewRecorder()
			h.handleDocument(recorder, req)

			body, err := ioutil.ReadAll(recorder.Result().Body)
			So(err, ShouldBeNil)
			So(recorder.Result().StatusCode, ShouldEqual, 200)

			var meta DocumentMetadata
			err = json.Unmarshal(body, &meta)
			So(err, ShouldBeNil)
			So(meta.Filename, ShouldStartWith, "dropbox/")
			So(meta.PageCount, ShouldEqual, 2)
		})
	})
}

func Test_ListFilecache(t *testing.T) {
	Convey("Testing handleListFilecache()", t, func() {
		cache, _ := filecache.New(10, os.TempDir())
		cache.DownloadFunc = func(downloadRecord *filecache.DownloadRecord, localPath string) error {
			return nil
		}

		rasterCache, _ := NewRasterCache(1)
		h := &RasterHttpServer{
			cache:       cache,
			rasterCache: rasterCache,
		}

		urlS3, _ := url.Parse("/documents/somewhere/sample.pdf")
		drS3, _ := filecache.NewDownloadRecord(urlS3.Path, nil)
		err := os.MkdirAll(filepath.Dir(cache.GetFileName(drS3)), 0755)
		So(err, ShouldBeNil)
		err = CopyFile(cache.GetFileName(drS3), "fixtures/sample.pdf", 0644)
		So(err, ShouldBeNil)

		urlDropbox, _ := url.Parse("/documents/dropbox/sample.pdf")
		drDropbox, _ := filecache.NewDownloadRecord(urlDropbox.Path, nil)
		err = os.MkdirAll(filepath.Dir(cache.GetFileName(drDropbox)), 0755)
		So(err, ShouldBeNil)
		err = CopyFile(cache.GetFileName(drDropbox), "fixtures/sample.pdf", 0644)
		So(err, ShouldBeNil)

		Reset(func() {
			os.Remove(cache.GetFileName(drS3))
			os.Remove(cache.GetFileName(drDropbox))
		})

		Convey("Handles a normal request when a few files are in the cache", func() {
			recorder := httptest.NewRecorder()

			req := httptest.NewRequest("GET", urlS3.Path, nil)
			h.handleDocument(recorder, req)
			So(recorder.Result().StatusCode, ShouldEqual, 200)

			req = httptest.NewRequest("GET", urlDropbox.Path, nil)
			h.handleDocument(recorder, req)
			So(recorder.Result().StatusCode, ShouldEqual, 200)

			recorder = httptest.NewRecorder()
			h.handleListFilecache(recorder, nil)
			So(recorder.Result().StatusCode, ShouldEqual, 200)

			body, err := ioutil.ReadAll(recorder.Result().Body)
			So(err, ShouldBeNil)

			cacheEntries := []FilecacheEntry{}
			err = json.Unmarshal(body, &cacheEntries)
			So(err, ShouldBeNil)
			So(len(cacheEntries), ShouldEqual, 2)
			So(cacheEntries[0].Path, ShouldEqual, "somewhere/sample.pdf")
			So(cacheEntries[0].StoragePath, ShouldEndWith, "12/c3e2cc0a00a4f64dfce9da6647d9ad84.pdf")
			So(cacheEntries[0].LoadedInMemory, ShouldBeFalse)
			So(cacheEntries[1].Path, ShouldEqual, "dropbox/sample.pdf")
			So(cacheEntries[1].StoragePath, ShouldEndWith, "8f/880c3eeebde773ca3e3af30f3e175c90.pdf")
			So(cacheEntries[1].LoadedInMemory, ShouldBeTrue)
		})

		Convey("Returns an empty result set when nothing is in the cache", func() {
			recorder := httptest.NewRecorder()
			h.handleListFilecache(recorder, nil)
			So(recorder.Result().StatusCode, ShouldEqual, 200)

			body, err := ioutil.ReadAll(recorder.Result().Body)
			So(err, ShouldBeNil)
			So(string(body), ShouldEqual, "[]")
		})
	})
}
