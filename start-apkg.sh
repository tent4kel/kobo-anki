#!/bin/sh
# Standalone apkg importer — no Nickel management, runs alongside Nickel
# like start-vocab.sh. Scans apkg/ for .apkg files dropped there (via USB,
# FTP, Dropbox, etc.) and imports each one into a deck named after the
# file, under the flashcard data directory.
#
# A successfully imported file moves to apkg/imported/ so it isn't
# reprocessed on the next run. This is a courtesy, not a correctness
# requirement: cmd/apkgimport's importGroup already merges by card ID and
# archives the source .apkg into the deck's own media folder, so re-running
# it on the same file is safe - it would just re-parse and re-copy media
# for no reason. A failed import is left in place so the next run retries it.
cd "$(dirname "$0")"
mkdir -p logs apkg apkg/imported
LOG=logs/apkg.log

DATA_DIR=words
if [ -f config/anki-core.conf ]; then
    val=$(grep -E '^[[:space:]]*data_dir[[:space:]]*=' config/anki-core.conf | tail -1 | cut -d= -f2 | tr -d '[:space:]')
    [ -n "$val" ] && DATA_DIR="$val"
fi

echo "=== start-apkg $(date) ===" >>"$LOG"

FOUND=0
IMPORTED=0
FAILED=0

for f in apkg/*.apkg; do
    [ -e "$f" ] || continue # no matches - the glob stayed literal
    FOUND=$((FOUND + 1))
    base=$(basename "$f")
    deck="${base%.apkg}"
    echo "Importing $base as deck \"$deck\"..." >>"$LOG"
    if ./bin/kobo-anki-apkgimport -apkg "$f" -deck "$deck" -datadir "$DATA_DIR" >>"$LOG" 2>&1; then
        mv "$f" "apkg/imported/$base"
        IMPORTED=$((IMPORTED + 1))
        echo "  OK -> $DATA_DIR/$deck.csv" >>"$LOG"
    else
        FAILED=$((FAILED + 1))
        echo "  FAILED (see above), left in place for retry" >>"$LOG"
    fi
done

if [ "$FOUND" -eq 0 ]; then
    MSG="No .apkg files found in apkg/"
elif [ "$FAILED" -eq 0 ]; then
    MSG="Imported $IMPORTED deck(s)"
else
    MSG="Imported $IMPORTED, $FAILED failed (see logs/apkg.log)"
fi
echo "$MSG" >>"$LOG"
./bin/fbink -q -m -y 10 "$MSG" 2>/dev/null

[ "$FAILED" -eq 0 ]
