#!/bin/sh

# If ${RASTER_BASE_DIR} is volume mounted on the host, then we namespace
# the cache folder using the current container ID (which can be read from
# ${HOSTNAME}) and then we use the container_stopped file as a flag to
# indicate whether we should clean the contents of any cache folders
# from any previous containers when we start again on the same host.
# The cleanup will be performed by lazyraster.svc
if [ -d "${RASTER_BASE_DIR}/${HOSTNAME}" ]; then
	touch "${RASTER_BASE_DIR}/${HOSTNAME}/container_stopped"
fi