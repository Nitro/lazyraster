package main

import (
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
	"strings"
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

func Test_EndToEnd(t *testing.T) {
	Convey("End-to-end testing handleDocument()", t, func() {
		didDownload := false
		downloadCount := 0
		downloadShouldSleep := false
		downloadShouldError := false
		var countLock sync.Mutex

		mockDownloader := func(downloadRecord *filecache.DownloadRecord, localPath string) error {
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

		filename := "12/6090c594d41728a7d7ad1e1a4d58cd28.pdf"      // cache file for sample.pdf
		filenameNoExt := "4d/6090c594d41728a7d7ad1e1a4d58cd28.pdf" // cache file for sample

		Reset(func() {
			os.Remove(cache.GetFileName(&filecache.DownloadRecord{Path: "somewhere/sample.pdf"}))
			os.Remove(cache.GetFileName(&filecache.DownloadRecord{Path: "somewhere/sample"}))
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
				os.MkdirAll(filepath.Join(os.TempDir(), filepath.Dir(filename)), 0755)
				CopyFile(cache.GetFileName(&filecache.DownloadRecord{Path: "somewhere/sample.pdf"}), "fixtures/sample.pdf", 0644)

				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=10", nil)
				recorder := httptest.NewRecorder()

				h.handleDocument(recorder, req)
				So(recorder.Result().StatusCode, ShouldEqual, 404)
			})

			Convey("When the page is not valid", func() {
				os.MkdirAll(filepath.Join(os.TempDir(), filepath.Dir(filename)), 0755)
				CopyFile(cache.GetFileName(&filecache.DownloadRecord{Path: "somewhere/sample.pdf"}), "fixtures/sample.pdf", 0644)

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
				os.MkdirAll(filepath.Join(os.TempDir(), filepath.Dir(filename)), 0755)
				CopyFile(cache.GetFileName(&filecache.DownloadRecord{Path: "somewhere/sample.pdf"}), "fixtures/sample.pdf", 0644)

				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1&width=-300", nil)
				recorder := httptest.NewRecorder()

				h.handleDocument(recorder, req)

				body, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(recorder.Result().StatusCode, ShouldEqual, 400)
				So(string(body), ShouldContainSubstring, "Invalid width")
			})

			Convey("Doesn't accept crazy wide width", func() {
				os.MkdirAll(filepath.Join(os.TempDir(), filepath.Dir(filename)), 0755)
				CopyFile(cache.GetFileName(&filecache.DownloadRecord{Path: "somewhere/sample.pdf"}), "fixtures/sample.pdf", 0644)

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
			os.MkdirAll(filepath.Join(os.TempDir(), filepath.Dir(filename)), 0755)
			os.MkdirAll(filepath.Join(os.TempDir(), filepath.Dir(filenameNoExt)), 0755)
			CopyFile(cache.GetFileName(&filecache.DownloadRecord{Path: "somewhere/sample.pdf"}), "fixtures/sample.pdf", 0644)
			CopyFile(cache.GetFileName(&filecache.DownloadRecord{Path: "somewhere/sample"}), "fixtures/sample.pdf", 0644)

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

			Convey("Sets the request HTTP headers in the DownloadRecord Args for recognised args", func() {
				req := httptest.NewRequest("GET", "/documents/somewhere/sample.pdf?page=1", nil)
				dummyArg := "DropboxAccessToken"
				dummyVal := "ThouShaltNotPass"
				req.Header.Add(dummyArg, dummyVal)

				isDummyArgSet := false
				cache.DownloadFunc = func(downloadRecord *filecache.DownloadRecord, localPath string) error {
					for arg, val := range downloadRecord.Args {
						if arg == strings.ToLower(dummyArg) && val == dummyVal {
							isDummyArgSet = true
						}
					}
					return mockDownloader(downloadRecord, localPath)
				}

				h.handleDocument(recorder, req)
				_, err := ioutil.ReadAll(recorder.Result().Body)
				So(err, ShouldBeNil)
				So(isDummyArgSet, ShouldBeTrue)
			})

			Convey("Fetches the file again if the recognised args differ", func() {
				dummyToken := "DropboxAccessToken"
				dummyTokenVal := "ThouShaltNotPass"
				url, _ := url.Parse("/documents/dropbox/sample.pdf")

				dr, _ := filecache.NewDownloadRecord(url.Path, map[string]string{dummyToken: dummyTokenVal})
				os.MkdirAll(filepath.Dir(cache.GetFileName(dr)), 0755)
				CopyFile(cache.GetFileName(dr), "fixtures/sample.pdf", 0644)

				defer os.Remove(cache.GetFileName(dr))

				req := httptest.NewRequest("GET", url.Path, nil)
				req.Header.Set(dummyToken, dummyTokenVal)

				h.handleDocument(recorder, req)
				So(recorder.Result().StatusCode, ShouldEqual, 200)
				So(downloadCount, ShouldEqual, 1)

				// It should be in the cache now
				h.handleDocument(recorder, req)
				So(recorder.Result().StatusCode, ShouldEqual, 200)
				So(downloadCount, ShouldEqual, 1)

				// We should download the file again if we use a different token
				req.Header.Set(dummyToken, "SaysWho?")
				h.handleDocument(recorder, req)
				So(recorder.Result().StatusCode, ShouldEqual, 200)
				So(downloadCount, ShouldEqual, 2)
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
			filename := cache.GetFileName(&filecache.DownloadRecord{Path: "somewhere/sample.pdf"})
			os.MkdirAll(filepath.Dir(filename), 0755)
			CopyFile(filename, "fixtures/sample.pdf", 0644)
			recorder := httptest.NewRecorder()

			cache.Cache.Add("somewhere/sample.pdf", filename)
			// On reload the file gets evicted/deleted so we need to put it back
			reloadableDownloader := func(downloadRecord *filecache.DownloadRecord, localPath string) error {
				CopyFile(filename, "fixtures/sample.pdf", 0644)
				return mockDownloader(downloadRecord, localPath)
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
	})
}
