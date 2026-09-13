#!/bin/sh
# Test fixture for Supervisor.Ensure: exits immediately on its first
# invocation (simulating a crashed decoder), then behaves exactly like
# fake_decoder.sh on every invocation after that.
#
# Invocation count is tracked in a file under $PYTHONPATH. This reuses the
# Decoder.ModulePath field (which the supervisor always exports as
# PYTHONPATH) purely as a scratch directory the test controls; it is not
# read as a Python module path by this fixture.

count_file="$PYTHONPATH/fake_decoder_once.count"
n=0
if [ -f "$count_file" ]; then
    n=$(cat "$count_file")
fi
n=$((n + 1))
echo "$n" >"$count_file"

if [ "$n" -eq 1 ]; then
    exit 0
fi

dir=$(dirname "$0")
exec "$dir/fake_decoder.sh"
