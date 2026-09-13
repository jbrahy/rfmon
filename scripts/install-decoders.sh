#!/usr/bin/env bash
# Builds and installs the rfmon WiFi and Bluetooth decoders: gr-foo,
# gr-ieee802-11 (the GNU Radio out-of-tree modules decoders/wifi_ofdm_rx.py
# imports), and ice9-bluetooth-sniffer (the ice9-bluetooth binary). This is
# a one-time setup step, run by hand before using rfmon without
# -spectrum-only; rfmon itself does not run this script.
#
# Usage: scripts/install-decoders.sh
# Environment: RFMON_DECODERS overrides the install prefix (default
# $HOME/.local/share/rfmon/decoders).
#
# Requires Homebrew and no sudo. Safe to re-run: each run clones and builds
# into a fresh temporary directory and installs on top of the same prefix.
set -euo pipefail

PREFIX="${RFMON_DECODERS:-$HOME/.local/share/rfmon/decoders}"
GR_PYTHON="/opt/homebrew/opt/gnuradio/libexec/venv/bin/python"
SITE_PACKAGES="$PREFIX/lib/python3.14/site-packages"

GR_FOO_REPO="https://github.com/bastibl/gr-foo.git"
GR_FOO_COMMIT="4c2a471b0453b9dca669b2d9dfcbfba6278741d7"
GR_IEEE_REPO="https://github.com/bastibl/gr-ieee802-11.git"
GR_IEEE_COMMIT="ad0598e4a874f4b8e1f391a1e0323e80df2b34ff"
ICE9_REPO="https://github.com/mikeryan/ice9-bluetooth-sniffer.git"
ICE9_COMMIT="788c5bce998a2d15c6b26a7d729ad81a5d28a869"

echo "Installing rfmon decoders into $PREFIX"

echo "==> brew install gnuradio liquid-dsp pybind11 cmake"
brew install gnuradio liquid-dsp pybind11 cmake

mkdir -p "$PREFIX"

BUILD_DIR="$(mktemp -d)"
cleanup() {
    rm -rf "$BUILD_DIR"
}
trap cleanup EXIT

clone_at() {
    local repo="$1"
    local commit="$2"
    local dest="$3"
    git clone "$repo" "$dest"
    git -C "$dest" checkout "$commit"
}

echo "==> Fetching gr-foo at $GR_FOO_COMMIT"
clone_at "$GR_FOO_REPO" "$GR_FOO_COMMIT" "$BUILD_DIR/gr-foo"

echo "==> Fetching gr-ieee802-11 at $GR_IEEE_COMMIT"
clone_at "$GR_IEEE_REPO" "$GR_IEEE_COMMIT" "$BUILD_DIR/gr-ieee802-11"

echo "==> Fetching ice9-bluetooth-sniffer at $ICE9_COMMIT"
clone_at "$ICE9_REPO" "$ICE9_COMMIT" "$BUILD_DIR/ice9-bluetooth-sniffer"

# gr-ieee802-11 needs gr-foo already installed under PREFIX, so PREFIX must
# come before /opt/homebrew on the prefix path for both builds.
build_gr_module() {
    local src="$1"
    local build="$src/build"
    mkdir -p "$build"
    cmake -S "$src" -B "$build" \
        -DCMAKE_INSTALL_PREFIX="$PREFIX" \
        -DCMAKE_PREFIX_PATH="$PREFIX;/opt/homebrew" \
        -DENABLE_DOXYGEN=OFF
    make -C "$build" -j
    make -C "$build" install
}

echo "==> Building gr-foo"
build_gr_module "$BUILD_DIR/gr-foo"

echo "==> Building gr-ieee802-11"
build_gr_module "$BUILD_DIR/gr-ieee802-11"

echo "==> Building ice9-bluetooth-sniffer"
ICE9_SRC="$BUILD_DIR/ice9-bluetooth-sniffer"
ICE9_BUILD="$ICE9_SRC/build"
mkdir -p "$ICE9_BUILD"
# USE_VKFFT=OFF: the Metal/VkFFT build fails to link on macOS 26. The fftw
# path (via /opt/homebrew) works.
cmake -S "$ICE9_SRC" -B "$ICE9_BUILD" \
    -DUSE_VKFFT=OFF \
    -DCMAKE_INSTALL_PREFIX="$PREFIX" \
    -DCMAKE_PREFIX_PATH="/opt/homebrew"
make -C "$ICE9_BUILD" -j
make -C "$ICE9_BUILD" install

echo "==> Verifying install"
PYTHONPATH="$SITE_PACKAGES" "$GR_PYTHON" -c "import ieee802_11, foo"
"$PREFIX/bin/ice9-bluetooth" -h >/dev/null

echo
echo "Decoders installed at $PREFIX"
echo "Pass these flags to rfmon:"
echo "  -decoders $PREFIX -gr-python $GR_PYTHON"
