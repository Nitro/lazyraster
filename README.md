Lazyraster
==========
A caching PDF rasterizer that uses a filecache and a hashring to distribute load.

MuPDF is the PDF engine that drives the rasterizer.

License Restriction
-------------------

This project is released under an [MIT license](LICENSE), but it relies on the AGPL code from [lazypdf](https://github.com/Nitro/lazypdf). Therefore, running it must comply with the terms and conditions from [this license](https://github.com/Nitro/lazypdf/blob/master/LICENSE). Sorry about that!

Building It
-----------

**You MUST first install and build [LazyPdf](https://github.com/Nitro/lazypdf)**

This requires some C library dependencies that are not vendored in this
project. Install it in the correct location in your `GOPATH` so that it
can be found in the correct location when building this project.

Once that is installed, if you have not already, you will need to install
all of the dependencies in `vendor/`. This project uses the `dep` tool to manage
dependencies. You must have that installed:
```
go get github.com/golang/dep/cmd/dep
```

Installing the dependencies is:
```
dep ensure
```

Updating a package dependency using dep. Example with
[Nitro/filecache](https://github.com/Nitro/filecache) package:
```
dep ensure -update github.com/Nitro/filecache
```

You can then build this project builds with a simple `go build`.

Running It
----------

Simply call the executable. By default, it will run on port 8000 and serve pdfs
located in the current directory. Configuration is done using environment
variables. These include the following:

 * `RASTER_BASE_DIR`: The location where cached files are to be stored and served (default `.`)
 * `RASTER_HTTP_PORT`: The port to listen on for HTTP connections (default `8000`)
 * `RASTER_AWS_REGION`: The AWS Region fallback to use when S3 region lookup fails (default `eu-central-1`)
 * `RASTER_CACHE_SIZE`: The number of file objects to cache on disk at any one time. (default `512`)
 * `RASTER_URL_SIGNING_SECRET`: A secret to use when validating signed URLs (default: `deadbeef`). Set it to empty string to disable signature validation.
 * `RASTER_RASTER_CACHE_SIZE`: The number of Rasterizer objects to cache in memory at any one time (default `20`)
 * `RASTER_RASTER_BUFFER_SIZE`: The maximum number of raster requests to queue (default `10`)
 * `RASTER_LOGGING_LEVEL`: The cut off level for log messages. (`debug`, `info`, `warn`, `error`, default `info`)

In addition, the AWS APIs will require authorization in the form of the standard
AWS environment variables:

 * `AWS_ACCESS_KEY_ID`
 * `AWS_SECRET_ACCESS_KEY`

Local Development
-----------------

If you are running this locally for development purposes, you will probably
want to use the following options to get started:

```bash
$ RASTER_URL_SIGNING_SECRET="" RASTER_LOGGING_LEVEL=debug ./lazyraster
```

Copyright
---------

Copyright (c) 2017-2020 Nitro Software.
