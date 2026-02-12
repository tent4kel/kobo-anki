#!/bin/sh
# Kobo Anki launcher (debug) — kills Nickel, feeds watchdog.
# Does NOT reboot on exit (use 'reboot' manually when done debugging).
cd "$(dirname "$0")"
LOG=kobo-anki.log
echo "=== start-debug $(date) ===" >>$LOG

WD_PID=""

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

# Debug cleanup: stop watchdog feeder but do NOT reboot.
# Watchdog will trigger a reboot after ~60s unless you run 'reboot' yourself.
cleanup() {
    echo "Cleanup at $(date)" >>$LOG
    if [ -n "$WD_PID" ]; then
        kill "$WD_PID" 2>/dev/null
        wait "$WD_PID" 2>/dev/null
    fi
    sync
    echo "Watchdog stopped. Run 'reboot' to return to Nickel." >>$LOG
    echo "Watchdog stopped. Run 'reboot' to return to Nickel."
}
trap cleanup EXIT INT TERM

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
start_watchdog

./bin/kobo-vocab -conf anki-mywords.conf 2>&1 | tee -a $LOG

./bin/fbink -c -f
DEBUG=1 ./bin/kobo-anki-fbink 2>&1 | tee -a $LOG

# cleanup runs via EXIT trap — prints reminder to reboot
