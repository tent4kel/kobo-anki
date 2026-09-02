#!/bin/sh
cd "$(dirname "$0")"
mkdir -p logs

# Translate new vocabulary words
./bin/kobo-vocab -conf config/anki-mywords.conf 2>>logs/vocab.log

# Bring up loopback and start server
ifconfig lo 127.0.0.1 2>/dev/null
./bin/kobo-anki-server
