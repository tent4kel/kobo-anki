#!/bin/sh
# Build a kobo-anki-<version>.zip release archive: extract it and copy the
# kobo-anki/ folder it contains onto the device's .adds/ (USB mass storage
# or an SD card reader - no FTP or shell access needed). This is the
# "proper archive" counterpart to deploy-ftp.sh, which instead pushes
# straight to a running device over FTP for iterative dev use.
#
# Kobo's own boot-time install convention is a KoboRoot.tgz dropped in
# .kobo/, which Nickel's update-check extracts onto the root filesystem as
# root - we don't use that. This app runs from .adds/ like any other
# NickelMenu-launched homebrew app: nothing installs or runs automatically,
# you still need NickelMenu (a separate prerequisite) to add the menu entry
# that runs start.sh. See docs/installation.md.
#
# Usage:
#   ./build-release.sh [version]
#   version defaults to `git describe --tags` (e.g. v1.2.0), or "dev" if
#   the repo has no tags reachable from HEAD.
set -e
cd "$(dirname "$0")"
REPO=$(pwd)

VERSION="${1:-$(git describe --tags 2>/dev/null || echo dev)}"
OUT="kobo-anki-${VERSION}.zip"

echo "Building ARM binaries..."
sh build.sh

echo ""
echo "Packaging $OUT..."

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
ROOT="$STAGE/kobo-anki"
mkdir -p "$ROOT/bin" "$ROOT/config" "$ROOT/fonts" "$ROOT/templates" "$ROOT/samples"

cp bin/kobo-anki-fbink bin/kobo-anki-apkgimport bin/kobo-anki-server bin/kobo-vocab "$ROOT/bin/"
# fbink itself, same caveat as deploy-ftp.sh: only included if you already
# have an image-support-enabled build in bin/ locally (see
# docs/installation.md) - this script doesn't fetch one for you.
if [ -f bin/fbink ]; then
	cp bin/fbink "$ROOT/bin/"
else
	echo "NOTE: bin/fbink not found locally - not included in the archive."
	echo "      Download it from https://github.com/NiLuJe/FBInk/releases and add it"
	echo "      to bin/kobo-anki/ on the device yourself."
fi

cp start.sh start-debug.sh start-fbink.sh start-server.sh start-vocab.sh start-apkg.sh "$ROOT/"

# Config ships as the .example templates, not this machine's own hand-edited
# anki-*.conf (which deploy-ftp.sh pushes to the maintainer's own test
# device, but has no business in a release meant for anyone else) - matches
# the "copy the .example files and rename them" step in docs/installation.md.
cp anki-core.conf.example "$ROOT/config/anki-core.conf"
cp anki-fbink.conf.example "$ROOT/config/anki-fbink.conf"
cp anki-mywords.conf.example "$ROOT/config/anki-mywords.conf"
[ -f anki-apkgimport.conf.example ] && cp anki-apkgimport.conf.example "$ROOT/config/anki-apkgimport.conf"
cp cacert.pem "$ROOT/config/"
cp cards.css "$ROOT/config/"

cp fonts/*.ttf fonts/OFL.txt "$ROOT/fonts/"
cp templates/*.html "$ROOT/templates/"
[ -f french.csv ] && cp french.csv "$ROOT/samples/"

rm -f "$REPO/$OUT"
(cd "$STAGE" && zip -qr "$REPO/$OUT" kobo-anki)

echo ""
echo "Built $OUT"
echo "Extract it and copy the kobo-anki/ folder into .adds/ on the device."
