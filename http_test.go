package main

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func Test_urlToFilename(t *testing.T) {
	Convey("urlToFilename()", t, func() {
		Convey("Strips leading '/documents'", func() {
			fn := urlToFilename("/documents/testing-bucket/foo-file.pdf")

			So(fn, ShouldNotContainSubstring, "/documents")
		})

		// TODO This is temporary! Remove when migrated.
		Convey("Strips the bucket name from the path", func() {
			fn := urlToFilename("/documents/testing-bucket/foo-file.pdf")
			So(fn, ShouldNotContainSubstring, "/testing-bucket")
		})

		Convey("Does not return a leading slash", func() {
			fn := urlToFilename("/documents/testing-bucket/foo-file.pdf")
			So(fn, ShouldEqual, "foo-file.pdf")
		})
	})
}
