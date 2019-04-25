FROM ubuntu:16.04

RUN apt-get update && apt-get install -y \
                                curl \
                                ca-certificates

# Install S6 from static bins
RUN cd / && curl -L https://github.com/just-containers/skaware/releases/download/v1.17.1/s6-eeb0f9098450dbe470fc9b60627d15df62b04239-linux-amd64-bin.tar.gz | tar -xvzf -

ADD s6 /etc

ADD lazyraster /lazyraster/lazyraster

# Mount a host volume to this folder when running the container
ENV RASTER_BASE_DIR="/lazyraster/cache"

# Expose ports for Redis, Memberlist and Raster
EXPOSE 6379 7946/tcp 7946/udp 8000

CMD ["/bin/s6-svscan", "/etc/services"]