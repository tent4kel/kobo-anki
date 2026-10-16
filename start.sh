#!/bin/sh
# Kobo Anki launcher — kills Nickel, restarts it in place on exit.
# Designed to be launched from NickelMenu.
#
# Restarting Nickel in place (instead of rebooting) was tried and reverted
# twice historically: once in commit 6dae13f ("leaves the device in a bad
# state"), and again in a 2026-08-29/30 session that reimplemented it
# carefully against koreader.sh/nickel.sh and Plato/Cadmus's own scripts,
# fixed real bugs along the way (missing env on restart, watchdog-fd
# handling), and still hit repeated silent Nickel crashes on real hardware -
# including through Cadmus's own unmodified, unrelated nickel.sh, badly
# enough once to trip Kobo's firmware failsafe (renamed libnm.so to
# .failsafe after repeated boot-time crashes).
#
# Both failures predate discovering (2026-10-16) that this device is a
# P365/code-395 "Clara BW (2025 refresh)" - a different hardware revision
# than most restart-handling reference code (koreader.sh, Plato/Cadmus's
# nickel.sh) was presumably written against. Confirmed working here via a
# direct device test the same day, after finding and replicating what
# BandarLabs/Cobalt (github.com/BandarLabs/Cobalt) does differently: it
# stops Kobo's freeze-watchdog supervisor (sickel) *before* Nickel, not at
# the same time, and restarts only Nickel - which spawns its own fresh
# sickel on startup. See the kobo-nickel-restart-unsafe project memory for
# the full history and the test script this was validated with.
#
# This still falls back to a reboot if Nickel doesn't come back and *stay*
# back - see restore_nickel's health check below. Nothing here has been
# proven for hindenburg/fickel/adobehost/dhcpcd*/fmon specifically (only
# nickel+sickel were isolated in testing, run independently of this app's
# own lifecycle) - if one of those turns out to matter, that shows up as
# Nickel failing the health check and rebooting, not as a stranded device.
#
# No /dev/watchdog handling: it's held exclusively by an in-kernel
# `watchdogd` thread on this device (confirmed via /proc/<pid>/status:
# PPid 2, a child of kthreadd) that pets it regardless of userspace - a
# userspace open always fails EBUSY here.
cd "$(dirname "$0")"
mkdir -p logs
LOG="$(pwd)/logs/kobo-anki.log"
echo "=== start $(date) ===" >>$LOG

READER_EXE="/usr/local/Kobo/nickel"
SUPERVISOR_EXE="/usr/local/Kobo/sickel"
NICKEL_STATE="/tmp/kobo-anki-nickel-state"
START_GRACE=45
HEALTH_CHECK_DELAY=5

# find_pid_by_zeroth_arg EXE - the pid whose argv[0] is exactly EXE (an
# absolute path), never a name search (pidof-style), which could signal an
# unrelated process.
find_pid_by_zeroth_arg() {
    exe="$1"
    found=""
    for p in /proc/[0-9]*; do
        pid=${p#/proc/}
        [ -r "$p/cmdline" ] || continue
        arg0=$(tr '\0' '\n' <"$p/cmdline" 2>/dev/null | head -n1)
        if [ "$arg0" = "$exe" ]; then
            [ -n "$found" ] && return 1
            found="$pid"
        fi
    done
    [ -n "$found" ] || return 1
    echo "$found"
}

# capture_nickel PID - saves environment, remaining argv, and cwd before
# Nickel is killed below, so restore_nickel can start an identical process
# later rather than a bare "$READER_EXE &" with this script's own (wrong)
# environment - the blank-environment bug the 2026-08-29/30 attempt found
# and fixed.
capture_nickel() {
    pid="$1"
    mkdir -p "$NICKEL_STATE"
    tr '\0' '\n' <"/proc/$pid/environ" >"$NICKEL_STATE/environ.txt" 2>/dev/null
    tr '\0' '\n' <"/proc/$pid/cmdline" >"$NICKEL_STATE/cmdline.txt" 2>/dev/null
    readlink "/proc/$pid/cwd" >"$NICKEL_STATE/cwd.txt" 2>/dev/null
}

restart_nickel() {
    (
        if [ -f "$NICKEL_STATE/environ.txt" ]; then
            while IFS= read -r line; do
                case "$line" in *=*) export "$line" ;; esac
            done <"$NICKEL_STATE/environ.txt"
        fi
        if [ -f "$NICKEL_STATE/cwd.txt" ] && cwd=$(cat "$NICKEL_STATE/cwd.txt" 2>/dev/null) && [ -n "$cwd" ]; then
            cd "$cwd" 2>/dev/null
        fi
        set --
        if [ -s "$NICKEL_STATE/cmdline.txt" ]; then
            first=1
            while IFS= read -r arg; do
                if [ "$first" = 1 ]; then first=0; continue; fi
                set -- "$@" "$arg"
            done <"$NICKEL_STATE/cmdline.txt"
        fi
        exec "$READER_EXE" "$@"
    ) >/dev/null 2>&1 &
}

wait_for_nickel() {
    grace="$1"
    waited=0
    while [ "$waited" -lt "$grace" ]; do
        find_pid_by_zeroth_arg "$READER_EXE" >/dev/null 2>&1 && return 0
        sleep 1
        waited=$((waited + 1))
    done
    return 1
}

# restore_nickel - the exit-time counterpart to the kill below. Stops
# sickel first if it's running (the thing under test - see the header
# comment), then starts Nickel from the state captured before it was
# killed. Only calls it a success if Nickel is still alive a few seconds
# later: the earlier failed attempts' signature was "crashed a short while
# after a successful-looking restart", so a bare pid check right after
# starting it isn't enough on its own. Falls back to a reboot on any
# failure so the device is never left stranded without a reader.
restore_nickel() {
    echo "Restoring Nickel at $(date)" >>$LOG

    if sickel_pid=$(find_pid_by_zeroth_arg "$SUPERVISOR_EXE"); then
        kill -TERM "$sickel_pid" 2>/dev/null
        waited=0
        while [ "$waited" -lt 10 ] && kill -0 "$sickel_pid" 2>/dev/null; do
            sleep 1
            waited=$((waited + 1))
        done
        kill -0 "$sickel_pid" 2>/dev/null && kill -KILL "$sickel_pid" 2>/dev/null
    fi

    restart_nickel
    if ! wait_for_nickel "$START_GRACE"; then
        echo "Nickel did not come back within ${START_GRACE}s, rebooting" >>$LOG
        sync
        reboot
        return
    fi

    sleep "$HEALTH_CHECK_DELAY"
    if find_pid_by_zeroth_arg "$READER_EXE" >/dev/null 2>&1; then
        echo "Nickel restored and stable after ${HEALTH_CHECK_DELAY}s" >>$LOG
    else
        echo "Nickel died within ${HEALTH_CHECK_DELAY}s of restart, rebooting" >>$LOG
        sync
        reboot
    fi
}

# --- Cleanup: runs on any exit (normal, crash, signal) ---
cleanup() {
    restore_nickel
    rm -rf "$NICKEL_STATE"
}
trap cleanup EXIT INT TERM

# --- Stop Nickel ---
sync
if nickel_pid=$(find_pid_by_zeroth_arg "$READER_EXE"); then
    capture_nickel "$nickel_pid"
fi
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
