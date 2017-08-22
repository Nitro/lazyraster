#!/bin/bash

die() {
    echo $1
    exit 1
}

file ../lazyraster | grep "ELF.*LSB" || die "../lazyraster is missing or not a Linux binary"

cp ../lazyraster . && docker build -t lazyraster . || die "Failed to build"