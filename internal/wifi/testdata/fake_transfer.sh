#!/bin/sh
# Test fixture standing in for hackrf_transfer: writes N zero bytes to
# stdout, where N is the value following -n on the command line, then
# exits 0.

n=0
prev=""
for arg in "$@"; do
    if [ "$prev" = "-n" ]; then
        n="$arg"
    fi
    prev="$arg"
done

head -c "$n" /dev/zero
exit 0
