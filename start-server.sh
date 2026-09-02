#!/bin/sh
cd "$(dirname "$0")"

# Vocabulary lookup is not run here - use start-vocab.sh (its own NickelMenu
# entry) to check for and translate new words on demand.

# Bring up loopback and start server
ifconfig lo 127.0.0.1 2>/dev/null
./bin/kobo-anki-server
