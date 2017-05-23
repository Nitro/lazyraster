Lazyraster
==========

A caching PDF rasterizer that uses a filecache and a hashring to distribute load.

MuPDF is the PDF engine that drives the rasterizer.

Running It
----------

Simply call the executable. By default, it will run on port 8000 and serve pdfs
located in the current directory. Configuration is done using environment
variables. These include the following:

 * `RASTER_BASE_DIR`: The location where cached files are to be stored and served.
 * `RASTER_PORT`: The port to listen on for HTTP connections.
 * `RASTER_AWS_REGION`: The AWS Region to use when serving from an S3 bucket.
 * `RASTER_S3_BUCKET`: The backing S3 bucket to use for fetching files.
 * `RASTER_CLUSTER_SEEDS`: The seeds to use to start the gossip ring.

In addition, the AWS APIs will require authorization in the form of the standard
AWS environment variables:

 * `AWS_ACCESS_KEY_ID`
 * `AWS_SECRET_ACCESS_KEY`

Copyright
---------

Copyright (c) 2017 Nitro Software.
