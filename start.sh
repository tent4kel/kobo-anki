#!/bin/sh
# Kobo Anki launcher — kills Nickel, reboots on exit.
# Designed to be launched from NickelMenu.
#
# Restarting Nickel in place (instead of rebooting) was tried and reverted
# twice: once in commit 6dae13f ("leaves the device in a bad state"), and
# again in a 2026-08-29/30 session that reimplemented the restart carefully
# against koreader.sh/nickel.sh and Plato/Cadmus's own scripts, fixed real
# bugs along the way (missing env on restart, watchdog fd handling), and
# still hit repeated Nickel crashes on real hardware - including through
# Cadmus's own unmodified, unrelated nickel.sh, badly enough to trip Kobo's
# firmware failsafe (renamed libnm.so to .failsafe after repeated boot
# crashes). That's independent evidence the incompatibility is with
# restarting Nickel in place on this device, not with any one script's
# implementation. Do not retry this without new evidence.
#
# No watchdog handling here either: /dev/watchdog on this device is held
# exclusively by an in-kernel `watchdogd` thread (confirmed via
# /proc/<pid>/status: PPid 2, i.e. a child of kthreadd) that pets it
# regardless of what userspace does. A userspace open of /dev/watchdog always
# fails EBUSY here - matches why koreader.sh/nickel.sh don't bother with one.
cd "$(dirname "$0")"
mkdir -p logs
LOG="$(pwd)/logs/kobo-anki.log"
echo "=== start $(date) ===" >>$LOG

# --- Cleanup: runs on any exit (normal, crash, signal) ---
cleanup() {
    echo "Cleanup at $(date), rebooting..." >>$LOG
    sync
    reboot
}
trap cleanup EXIT INT TERM

# --- Stop Nickel ---
sync
killall -q -TERM nickel hindenburg sickel fickel adobehost dhcpcd-dbus dhcpcd fmon

kill_wait=0
while pkill -0 nickel 2>/dev/null; do
    [ "$kill_wait" -ge 16 ] && break
    usleep 250000
    kill_wait=$((kill_wait + 1))
done
echo "Nickel stopped (waited ${kill_wait}x250ms)" >>$LOG

rm -f /tmp/nickel-hardware-status

# --- Translate vocabulary (runs in background so the UI isn't blocked) ---
AUTO_PARSE=false
if [ -f config/anki-mywords.conf ]; then
    val=$(grep -E '^[[:space:]]*auto_parse[[:space:]]*=' config/anki-mywords.conf | tail -1 | cut -d= -f2 | tr -d '[:space:]')
    [ "$val" = "true" ] && AUTO_PARSE=true
fi

if [ "$AUTO_PARSE" = "true" ]; then
    # Fast, network-free pre-check: how many new words are actually pending
    # (see -count-new in kobo-vocab), so the startup toast can say something
    # more useful than a generic "updating" message - and so nothing runs
    # at all when there's genuinely nothing new to look up.
    NEW_COUNT=$(./bin/kobo-vocab -conf config/anki-mywords.conf -count-new 2>>$LOG)
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
            ./bin/kobo-vocab -conf config/anki-mywords.conf >>$LOG 2>&1
            RC=$?
            if [ "$RC" -eq 0 ]; then
                ./bin/fbink -q -m -y 10 "Vocabulary updated" 2>/dev/null
            else
                ./bin/fbink -q -m -y 10 "Vocabulary update failed (see log)" 2>/dev/null
            fi
        ) &
    else
        echo "No new words pending, skipping kobo-vocab" >>$LOG
    fi
else
    echo "auto_parse disabled in anki-mywords.conf, skipping kobo-vocab" >>$LOG
fi

# --- Main app with crash recovery ---
CRASH_COUNT=0
MAX_CRASHES=5

while true; do
    ./bin/fbink -c -f
    ./bin/kobo-anki-fbink >>$LOG 2>&1
    RC=$?

    # Clean exit
    [ "$RC" -eq 0 ] && break

    CRASH_COUNT=$((CRASH_COUNT + 1))
    echo "Crash #$CRASH_COUNT (exit=$RC) at $(date)" >>$LOG

    if [ "$CRASH_COUNT" -ge "$MAX_CRASHES" ]; then
        echo "Too many crashes, giving up" >>$LOG
        break
    fi

    ./bin/fbink -q -m -y 10 "Crash #$CRASH_COUNT, restarting..." 2>/dev/null
    sleep 2
done

# cleanup runs automatically via EXIT trap
