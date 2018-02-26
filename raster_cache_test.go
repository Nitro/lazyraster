package main

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

var (
	fixture = "fixtures/sample.pdf"
)

func Test_NewRasterCache(t *testing.T) {
	Convey("NewRasterCache()", t, func() {
		Convey("configures things properly", func() {
			cache, err := NewRasterCache(5, "")

			So(err, ShouldBeNil)
			So(cache, ShouldNotBeNil)
		})
	})
}

func Test_NewDefaultRasterCache(t *testing.T) {
	Convey("NewDefaultRasterCache()", t, func() {
		Convey("configures things properly", func() {
			cache, err := NewDefaultRasterCache()

			So(err, ShouldBeNil)
			So(cache, ShouldNotBeNil)
			So(cache.rasterizers.Len(), ShouldEqual, 0)
		})
	})
}

func Test_GetRasterizer(t *testing.T) {
	Convey("GetRasterizer()", t, func() {
		cache, _ := NewRasterCache(2, "")

		Convey("creates and stores a rasterizer", func() {
			raster, err := cache.GetRasterizer(fixture)

			So(err, ShouldBeNil)
			So(raster, ShouldNotBeNil)
		})

		Convey("returns the cached rasterizer when it's there", func() {
			raster, _ := cache.GetRasterizer(fixture)
			again, _ := cache.GetRasterizer(fixture)

			So(raster, ShouldEqual, again)
		})

		Convey("runs the rasterizer", func() {
			raster, _ := cache.GetRasterizer(fixture)

			err := raster.Run() // returns error when it's already started
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "has already been run")
		})
	})
}

func Test_Remove(t *testing.T) {
	Convey("Remove()", t, func() {
		Convey("removes a file", func() {
			cache, _ := NewRasterCache(1, "")

			raster, _ := cache.GetRasterizer(fixture)
			So(raster, ShouldNotBeNil)

			cache.Remove(fixture)

			raster2, _ := cache.GetRasterizer(fixture)
			So(raster2, ShouldNotBeNil)
			So(raster, ShouldNotEqual, raster2)
		})
	})
}

func Test_onEvicted(t *testing.T) {
	Convey("Handling eviction from the cache", t, func() {
		cache, err := NewRasterCache(2, "")
		raster, _ := cache.GetRasterizer(fixture)

		So(err, ShouldBeNil)
		So(cache.rasterizers.Len(), ShouldEqual, 1)

		cache.rasterizers.Remove(fixture)
		So(cache.mostRecentlyStopped, ShouldEqual, raster)
	})
}
