#!/bin/sh
# Build all binaries for Kobo (ARM7)
set -e
mkdir -p bin

echo "Building kobo-anki-fbink..."
GOOS=linux GOARCH=arm GOARM=7 go build -o bin/kobo-anki-fbink ./cmd/fbink

echo "Building kobo-anki-server..."
GOOS=linux GOARCH=arm GOARM=7 go build -o bin/kobo-anki-server ./cmd/server

echo "Building kobo-vocab..."
cd kobo-vocab
GOOS=linux GOARCH=arm GOARM=7 go build -o ../bin/kobo-vocab .
cd ..

echo ""
echo "Built: bin/kobo-anki-fbink, bin/kobo-anki-server, bin/kobo-vocab"
echo ""
echo "NOTE: You also need bin/fbink — download the Kobo build from:"
echo "  https://github.com/NiLuJe/FBInk/releases"
