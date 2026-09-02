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

# Vocabulary lookup is not run here - use start-vocab.sh (its own NickelMenu
# entry) to check for and translate new words on demand.

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
