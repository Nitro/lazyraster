FROM ubuntu:16.04

RUN apt-get update && apt-get install --no-install-recommends --yes curl=7.47.* ca-certificates=20210119~*

ADD lazyraster /lazyraster/lazyraster

ENTRYPOINT ["/lazyraster/lazyraster"]