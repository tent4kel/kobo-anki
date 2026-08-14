#!/bin/sh
# Build all binaries for Kobo (ARM7)
set -e
mkdir -p bin

# cmd/fbink and cmd/apkgimport import htmlrender, which cgo-compiles the
# vendored litehtml C++ engine - they need a real C/C++ cross-compiler, not
# just GOARCH=arm. cmd/server and kobo-vocab don't touch htmlrender and stay
# plain cross-compiled Go (CGO_ENABLED=0, same as before this existed).
#
# Rather than requiring a system arm-linux-gnueabihf toolchain, this bootstraps
# a pinned, standalone Zig into .build-tools/ on first run and reuses it after
# - `zig cc`/`zig c++ -target arm-linux-musleabihf` cross-compile C/C++ for
# ARM7 with no sysroot setup, and -static/musl means the resulting binary has
# no runtime dependency on whatever libc happens to be on the device, matching
# how these binaries are already deployed (drop a static binary via FTP, run
# it).
ZIG_VERSION=0.14.1
ZIG_DIR="$(pwd)/.build-tools/zig-x86_64-linux-${ZIG_VERSION}"
ZIG_BIN="${ZIG_DIR}/zig"

if [ ! -x "$ZIG_BIN" ]; then
	echo "Fetching Zig ${ZIG_VERSION} (one-time, for the litehtml cross-compile)..."
	mkdir -p .build-tools
	TARBALL=".build-tools/zig-${ZIG_VERSION}.tar.xz"
	# ziglang.org 404s a plain curl GET with no User-Agent on this tarball
	# (a HEAD without one works fine) - send a browser UA to avoid that.
	curl -sL -A "Mozilla/5.0" \
		"https://ziglang.org/download/${ZIG_VERSION}/zig-x86_64-linux-${ZIG_VERSION}.tar.xz" \
		-o "$TARBALL"
	tar -xf "$TARBALL" -C .build-tools
	rm -f "$TARBALL"
fi

CGO_CC="${ZIG_DIR}/zigcc-arm7.sh"
CGO_CXX="${ZIG_DIR}/zigcxx-arm7.sh"
cat >"$CGO_CC" <<EOF
#!/bin/sh
exec "$ZIG_BIN" cc -target arm-linux-musleabihf "\$@"
EOF
cat >"$CGO_CXX" <<EOF
#!/bin/sh
exec "$ZIG_BIN" c++ -target arm-linux-musleabihf "\$@"
EOF
chmod +x "$CGO_CC" "$CGO_CXX"

echo "Building kobo-anki-fbink..."
CGO_ENABLED=1 GOOS=linux GOARCH=arm GOARM=7 CC="$CGO_CC" CXX="$CGO_CXX" CGO_LDFLAGS=-static \
	go build -o bin/kobo-anki-fbink ./cmd/fbink

# apkgimport uses ankipkg (pure Go - modernc.org/sqlite has no cgo) and never
# imports htmlrender, so unlike cmd/fbink it needs no C compiler at all -
# same plain cross-compile as cmd/server/kobo-vocab below.
echo "Building kobo-anki-apkgimport..."
GOOS=linux GOARCH=arm GOARM=7 go build -o bin/kobo-anki-apkgimport ./cmd/apkgimport

echo "Building kobo-anki-server..."
GOOS=linux GOARCH=arm GOARM=7 go build -o bin/kobo-anki-server ./cmd/server

echo "Building kobo-vocab..."
GOOS=linux GOARCH=arm GOARM=7 go build -o bin/kobo-vocab ./kobo-vocab

echo ""
echo "Built: bin/kobo-anki-fbink, bin/kobo-anki-apkgimport, bin/kobo-anki-server, bin/kobo-vocab"
echo ""
echo "NOTE: You also need bin/fbink — download the Kobo build from:"
echo "  https://github.com/NiLuJe/FBInk/releases"
