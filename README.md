Lazyraster
==========

[![](https://travis-ci.org/Nitro/lazyraster.svg?branch=master)](https://travis-ci.org/Nitro/lazyraster)

A caching PDF rasterizer that uses a filecache and a hashring to distribute load.

MuPDF is the PDF engine that drives the rasterizer.

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

You can then build this project builds with a simple `go build`.

Running It
----------

Simply call the executable. By default, it will run on port 8000 and serve pdfs
located in the current directory. Configuration is done using environment
variables. These include the following:

 * `RASTER_BASE_DIR`: The location where cached files are to be stored and served.
 * `RASTER_PORT`: The port to listen on for HTTP connections.
 * `RASTER_ADVERTISE_PORT`: The advertised host port which gets mapped to RASTER_PORT.
 * `RASTER_AWS_REGION`: The AWS Region to use when serving from an S3 bucket.
 * `RASTER_S3_BUCKET`: The backing S3 bucket to use for fetching files.
 * `RASTER_CLUSTER_SEEDS`: The seeds to use to start the gossip ring.
 * `RASTER_CACHE_SIZE`: The number of Rasterizer objects to cache at any one time.
 * `RASTER_REDIS_PORT`: The port on which to serve Redis protocol traffic.
 * `RASTER_CLUSTER_NAME`: The name of the Memberlist cluster.
 * `MEMBERLIST_ADVERTISE_ADDR`: The IP / hostname advertised by Memberlist.
 * `RASTER_MEMBERLIST_ADVERTISE_PORT`: The port advertised by Memberlist.
 * `RASTER_MEMBERLIST_BIND_PORT`: The port which Memberlist binds to.

In addition, the AWS APIs will require authorization in the form of the standard
AWS environment variables:

 * `AWS_ACCESS_KEY_ID`
 * `AWS_SECRET_ACCESS_KEY`

Copyright
---------

Copyright (c) 2017 Nitro Software.
