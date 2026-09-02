#!/bin/sh
cd "$(dirname "$0")"

# Vocabulary lookup is not run here - use start-vocab.sh (its own NickelMenu
# entry) to check for and translate new words on demand.

# Clear and run
./bin/fbink -c -f
./bin/kobo-anki-fbink
./bin/fbink -c -f
