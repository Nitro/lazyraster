FROM ubuntu:14.04

RUN apt-get update
RUN apt-get install -y libjpeg62 zlib1g libjbig2dec0 libfreetype6 libpng12-0
RUN apt-get install -y ca-certificates
RUN mkdir /lazyraster
ADD lazyraster /lazyraster/lazyraster

EXPOSE 7777
WORKDIR /lazyraster
CMD /lazyraster/lazyraster
