package main

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

var (
	fixture        = "fixtures/sample.pdf"
	anotherFixture = "fixtures/mixed-sample.pdf"
)

func Test_NewRasterCache(t *testing.T) {
	Convey("NewRasterCache()", t, func() {
		Convey("configures things properly", func() {
			cache, err := NewRasterCache(5)

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
		cache, _ := NewRasterCache(2)

		Convey("creates and stores a rasterizer", func() {
			raster, err := cache.GetRasterizer(fixture, 1)

			So(err, ShouldBeNil)
			So(raster, ShouldNotBeNil)
		})

		Convey("returns the cached rasterizer when it's there", func() {
			raster, _ := cache.GetRasterizer(fixture, 1)
			again, _ := cache.GetRasterizer(fixture, 1)

			So(raster, ShouldEqual, again)
		})

		Convey("runs the rasterizer", func() {
			raster, _ := cache.GetRasterizer(fixture, 1)

			err := raster.Run() // returns error when it's already started
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "has already been run")
		})
	})
}

func Test_Remove(t *testing.T) {
	Convey("Remove()", t, func() {
		Convey("removes a file", func() {
			cache, err := NewRasterCache(1)
			So(err, ShouldBeNil)

			raster, err := cache.GetRasterizer(fixture, 1)
			So(err, ShouldBeNil)
			So(raster, ShouldNotBeNil)
			So(cache.rasterizers.Contains(fixture), ShouldBeTrue)

			cache.Remove(fixture)
			So(cache.rasterizers.Contains(fixture), ShouldBeFalse)

			raster, err = cache.GetRasterizer(fixture, 1)
			So(err, ShouldBeNil)
			So(raster, ShouldNotBeNil)
			So(cache.rasterizers.Contains(fixture), ShouldBeTrue)
		})
	})
}

func Test_onEvicted(t *testing.T) {
	Convey("Handling eviction from the cache", t, func() {
		cache, err := NewRasterCache(1)
		So(err, ShouldBeNil)

		raster1, err := cache.GetRasterizer(fixture, 1)
		So(err, ShouldBeNil)
		So(raster1, ShouldNotBeNil)
		So(cache.rasterizers.Contains(fixture), ShouldBeTrue)
		So(cache.rasterizers.Contains(anotherFixture), ShouldBeFalse)

		raster2, err := cache.GetRasterizer(anotherFixture, 1)
		So(err, ShouldBeNil)
		So(raster2, ShouldNotBeNil)
		So(cache.rasterizers.Contains(fixture), ShouldBeFalse)
		So(cache.rasterizers.Contains(anotherFixture), ShouldBeTrue)
	})
}
