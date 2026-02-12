#!/bin/sh
cd "$(dirname "$0")"

# Translate new vocabulary words
./bin/kobo-vocab -conf anki-mywords.conf 2>>vocab.log

# Clear and run
./bin/fbink -c -f
./bin/kobo-anki-fbink
./bin/fbink -c -f
