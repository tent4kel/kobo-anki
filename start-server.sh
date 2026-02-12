#!/bin/sh
cd "$(dirname "$0")"

# Translate new vocabulary words
./bin/kobo-vocab -conf anki-mywords.conf 2>>vocab.log

# Bring up loopback and start server
ifconfig lo 127.0.0.1 2>/dev/null
./bin/kobo-anki-server
