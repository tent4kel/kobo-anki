#!/bin/sh
# Build the ARM binaries and push a full kobo-anki install to a Kobo over
# FTP, file by file (no remote shell/tar-extraction needed - just plain
# FTP STOR, which is all stock Kobo FTP servers support).
#
# Usage:
#   ./deploy-ftp.sh <kobo-ip> [target-folder]
#   ./deploy-ftp.sh                              (reads deploy.conf instead)
#
# target-folder is the directory name under .adds/ on the device, e.g.
# "kobo-anki" (production) or "kobo-anki-beta" (side-by-side test install).
# Defaults to "kobo-anki".
#
# Connection details can also be set in deploy.conf (gitignored, see
# deploy.conf.example) instead of passing them on the command line:
#   KOBO_HOST=192.168.1.116
#   KOBO_PORT=1021
#   KOBO_USER=root
#   KOBO_FOLDER=kobo-anki-beta
#
# Never touches words/ (your live flashcard progress) or logs/ on-device -
# only the files that ship in the tarball (bin/, templates/, config/,
# samples/, start*.sh) get uploaded/overwritten.
#
# Dictionaries are the one exception to "everything lands under the app
# folder": dict/*.zip go to /mnt/onboard/.kobo/custom-dict, which is Nickel's
# own, because that is what dict_dir points at. The whole point of putting
# them there is that the dictionary the Kobo uses to look a word up is the
# same file this tool later looks it up in. Anything already there under a
# different name is left alone; same-named files are overwritten.
set -e
cd "$(dirname "$0")"
REPO=$(pwd)

if [ -f deploy.conf ]; then
    . ./deploy.conf
fi

KOBO_HOST="${1:-${KOBO_HOST:-}}"
KOBO_FOLDER="${2:-${KOBO_FOLDER:-kobo-anki}}"
KOBO_PORT="${KOBO_PORT:-1021}"
KOBO_USER="${KOBO_USER:-root}"

if [ -z "$KOBO_HOST" ]; then
    echo "Usage: $0 <kobo-ip> [target-folder]"
    echo "   or: create deploy.conf (see deploy.conf.example) and run with no args"
    exit 1
fi

echo "Building ARM binaries..."
sh build.sh

echo ""
echo "Deploying to ftp://$KOBO_USER@$KOBO_HOST:$KOBO_PORT/.adds/$KOBO_FOLDER/"

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT

mkdir -p "$STAGE/bin" "$STAGE/templates" "$STAGE/config"
cp bin/kobo-anki-fbink bin/kobo-anki-apkgimport bin/kobo-anki-server bin/kobo-vocab "$STAGE/bin/"
# fbink itself is only uploaded if you already have one built/placed in
# bin/ locally - it needs an image-support-enabled build (see README), and
# this script won't silently overwrite a working device copy with nothing.
[ -f bin/fbink ] && cp bin/fbink "$STAGE/bin/"
cp start.sh start-debug.sh start-fbink.sh start-server.sh start-vocab.sh "$STAGE/"
cp anki-core.conf anki-fbink.conf anki-mywords.conf "$STAGE/config/"
# apkgimport's split_tags config - optional, most imports don't use it.
[ -f anki-apkgimport.conf ] && cp anki-apkgimport.conf "$STAGE/config/"
# CA bundle for the network providers' TLS verification - some Kobo
# firmware ships with no system CA store at all (see ca_cert in
# anki-mywords.conf.example), so this is deployed unconditionally.
cp cacert.pem "$STAGE/config/"
cp templates/*.html "$STAGE/templates/"
if [ -f french.csv ]; then
    mkdir -p "$STAGE/samples"
    cp french.csv "$STAGE/samples/"
fi
# Shared styling for html/apkg cards (core.InjectIntoHead) - not overwritten
# blindly by anything else, safe to redeploy after editing it.
[ -f cards.css ] && cp cards.css "$STAGE/config/"
# APIs/ (provider credentials, e.g. lexicala.json) - gitignored and only
# deployed if you've actually populated it; providers=lexicala on-device
# needs this to work at all.
if [ -d APIs ] && ls APIs/*.json >/dev/null 2>&1; then
    mkdir -p "$STAGE/config/APIs"
    cp APIs/*.json "$STAGE/config/APIs/"
fi

FTP_BASE="ftp://$KOBO_HOST:$KOBO_PORT/.adds/$KOBO_FOLDER"
cd "$STAGE"
find . -type f | while read -r f; do
    f="${f#./}"
    echo "  $f"
    curl -sf --max-time 30 -T "$f" --ftp-create-dirs "$FTP_BASE/$f" --user "$KOBO_USER:" || {
        echo "FAILED: $f" >&2
        exit 1
    }
done

# Dictionaries, to Nickel's custom-dict rather than the app folder - see the
# note at the top. Uploaded after the app so a half-finished deploy leaves the
# dictionaries as they were rather than the binaries.
cd "$REPO"
if ls dict/*.zip >/dev/null 2>&1; then
    echo ""
    echo "Dictionaries -> /mnt/onboard/.kobo/custom-dict/"
    for f in dict/*.zip; do
        echo "  $(basename "$f")"
        curl -sf --max-time 300 -T "$f" --ftp-create-dirs \
            "ftp://$KOBO_HOST:$KOBO_PORT/.kobo/custom-dict/$(basename "$f")" \
            --user "$KOBO_USER:" || {
            echo "FAILED: $f" >&2
            exit 1
        }
    done
fi

echo ""
echo "Done. Restart the app on-device (or re-run its NickelMenu entry) to pick up the new build."
