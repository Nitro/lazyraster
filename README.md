Lazyraster
==========

[![](https://travis-ci.org/Nitro/lazyraster.svg?branch=master)](https://travis-ci.org/Nitro/lazyraster)

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
 * `RASTER_ADVERTISE_HTTP_PORT`: The advertised host port which gets mapped to RASTER_HTTP_PORT (default `8000`)
 * `RASTER_AWS_REGION`: The AWS Region fallback to use when S3 region lookup fails (default `us-west-1`)
 * `RASTER_CLUSTER_SEEDS`: The seeds to use to start the gossip ring
 * `RASTER_CACHE_SIZE`: The number of file objects to cache on disk at any one time. (default `512`)
 * `RASTER_REDIS_PORT`: The port on which to serve Redis protocol traffic (default `6379`)
 * `RASTER_CLUSTER_NAME`: The name of the Memberlist cluster (default `default`)
 * `RASTER_RING_TYPE`: Use `sidecar` or `memberlist` backing for hash ring? (default: `sidecar`)
 * `RASTER_ADVERTISE_MEMBERLIST_HOST`: The IP / hostname advertised by Memberlist
 * `RASTER_ADVERTISE_MEMBERLIST_PORT`: The port advertised by Memberlist (default `7946`)
 * `RASTER_SIDECAR_URL`: The Sidecar state URL (default: `http://192.168.168.168:7777/api/state.json`)
 * `RASTER_SIDECAR_SERVICE_NAME`: The name to lookup in Sidecar when using Sidecar backing (default `lazyraster`)
 * `RASTER_SIDECAR_SERVICE_PORT`: The port to lookup in Sidecar when using Sidecar backing (default `10110`)
 * `RASTER_URL_SIGNING_SECRET`: A secret to use when validating signed URLs (default: `deadbeef`). Set it to empty string to disable signature validation.
 * `RASTER_RASTER_CACHE_SIZE`: The number of Rasterizer objects to cache in memory at any one time (default `20`)
 * `RASTER_RASTER_BUFFER_SIZE`: The maximum number of raster requests to queue (default `10`)
 * `RASTER_LOGGING_LEVEL`: The cut off level for log messages. (`debug`, `info`, `warn`, `error`, default `info`)

In addition, the AWS APIs will require authorization in the form of the standard
AWS environment variables:

 * `AWS_ACCESS_KEY_ID`
 * `AWS_SECRET_ACCESS_KEY`

If you are a New Relic customer and wish to monitor this using New Relic's
service, the service includes the
[Gorelic](https://github.com/yvasiyarov/gorelic) platform agent.  This is
currently used in place of the New Relic go agent due to [major licensing
issues](https://github.com/newrelic/go-agent/issues/45) with the current Go
agent. You may trigger the use of the New Relic agent by starting the service
with:

 * `NEW_RELIC_LICENSE_KEY`: the value is your current license key.
 * `SERVICE_NAME`: the name of the application in New Relic (e.g. 'foo-service')
 * `ENVIRONMENT_NAME`: appended to `SERVICE_NAME` (e.g. 'foo-service-prod')

Local Development
-----------------

If you are running this locally for development purposes, you will probably
want to use the following options to get started:

```bash
$ RASTER_RING_TYPE=memberlist RASTER_LOGGING_LEVEL=debug ./lazyraster
```

This will use the Memberlist clustering library and not require an external
Sidecar service discovery system. It will also enable debug logging level which
can help in understanding where things went wrong.

Copyright
---------

Copyright (c) 2017-2018 Nitro Software.
