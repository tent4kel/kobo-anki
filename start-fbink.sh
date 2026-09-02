#!/bin/sh
cd "$(dirname "$0")"
mkdir -p logs

# --- Translate vocabulary (runs in background so the UI isn't blocked) ---
# Mirrors start.sh's auto_parse gating - kept here since this script (unlike
# start.sh) never touches Nickel at all.
AUTO_PARSE=false
if [ -f config/anki-mywords.conf ]; then
    val=$(grep -E '^[[:space:]]*auto_parse[[:space:]]*=' config/anki-mywords.conf | tail -1 | cut -d= -f2 | tr -d '[:space:]')
    [ "$val" = "true" ] && AUTO_PARSE=true
fi

if [ "$AUTO_PARSE" = "true" ]; then
    # Fast, network-free pre-check: how many new words are actually pending
    # (see -count-new in kobo-vocab), so nothing runs at all when there's
    # genuinely nothing new to look up.
    NEW_COUNT=$(./bin/kobo-vocab -conf config/anki-mywords.conf -count-new 2>>logs/vocab.log)
    case "$NEW_COUNT" in
        ''|*[!0-9]*) NEW_COUNT=0 ;; # unexpected output (crash, etc.) - fall back safely
    esac

    if [ "$NEW_COUNT" -gt 0 ]; then
        if [ "$NEW_COUNT" -eq 1 ]; then
            MSG="1 new word found, starting lookup..."
        else
            MSG="$NEW_COUNT new words found, starting lookup..."
        fi
        ./bin/fbink -q -m -y 10 "$MSG" 2>/dev/null
        sleep 2 # give the e-ink screen time to actually show this before the app launch clears it
        (
            ./bin/kobo-vocab -conf config/anki-mywords.conf >>logs/vocab.log 2>&1
            RC=$?
            if [ "$RC" -eq 0 ]; then
                ./bin/fbink -q -m -y 10 "Vocabulary updated" 2>/dev/null
            else
                ./bin/fbink -q -m -y 10 "Vocabulary update failed (see log)" 2>/dev/null
            fi
        ) &
    fi
fi

# Clear and run
./bin/fbink -c -f
./bin/kobo-anki-fbink
./bin/fbink -c -f
