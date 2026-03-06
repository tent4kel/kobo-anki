#!/bin/sh
# Kobo Anki launcher — kills Nickel, feeds watchdog, reboots on exit.
# Designed to be launched from NickelMenu.
cd "$(dirname "$0")"
LOG=kobo-anki.log
echo "=== start $(date) ===" >>$LOG

WD_PID=""

# --- Watchdog management ---
start_watchdog() {
    if [ -e /dev/watchdog ]; then
        (
            exec 3>/dev/watchdog
            while true; do
                printf '.' >&3
                sleep 10
            done
        ) &
        WD_PID=$!
        echo "Watchdog feeder PID: $WD_PID" >>$LOG
    else
        echo "No /dev/watchdog found" >>$LOG
    fi
}

stop_watchdog() {
    if [ -n "$WD_PID" ]; then
        kill "$WD_PID" 2>/dev/null
        wait "$WD_PID" 2>/dev/null
        WD_PID=""
    fi
}

# --- Cleanup: runs on any exit (normal, crash, signal) ---
cleanup() {
    echo "Cleanup at $(date), rebooting..." >>$LOG
    stop_watchdog
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

# --- Take over watchdog ---
start_watchdog

# --- Translate vocabulary ---
./bin/kobo-vocab -conf anki-mywords.conf 2>&1 | tee -a $LOG

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
