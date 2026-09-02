#!/bin/sh
# Standalone kobo-vocab runner — no Nickel management at all (runs alongside
# Nickel, unlike start.sh/start-fbink.sh). Status shown via a NickelDBus modal
# dialog whose body is updated live from kobo-vocab's own log output, rather
# than a percentage progress bar - the dict-load step is one long opaque
# call, not N sub-steps kobo-vocab reports fine-grained progress for, so a
# real progress bar would just sit misleadingly at whatever value covers
# that phase. A Cancel button lets the lookup be stopped mid-run.
#
# NickelDBus usage verified live on-device (2026-09-01) via `qndb -a` and
# cross-checked against /mnt/onboard/.adds/zeitDownload/download.sh. Notes:
#  - No DBUS_SESSION_BUS_ADDRESS setup needed - NickelDBus is on the system
#    bus, qndb finds it on its own.
#  - dlgConfirmCreate is a small, fixed-size modal dialog, NOT a full-screen
#    overlay - confirmed live, nothing in the API resizes it.
#  - Setting only dlgConfirmSetReject (no SetAccept) shows a dialog with just
#    that one button - confirmed live, no default "OK" appears.
#  - dlgConfirmResult's value follows Qt's own QDialog convention: 0=Rejected
#    (Cancel), 1=Accepted - confirmed live by tapping each button in turn.
#  - Tapping a button closes the dialog itself - calling dlgConfirmClose
#    again afterwards fails ("dialog not open"), so cleanup below only closes
#    it when the run finished/errored on its own, not after a Cancel tap.
#  - `qndb -s <signal> -t <ms>` works as a standalone blocking listener
#    without a paired -m call - confirmed live.
#  - qndb's own stderr goes to qndb.log (not vocab.log), kept separate so it
#    can't get picked up as a "new line" by the live-tail loop below.
cd "$(dirname "$0")"
mkdir -p logs
LOG=logs/vocab.log
QNDB_LOG=logs/qndb.log
QNDB="/usr/bin/qndb"
CANCEL_OUT="/tmp/kobo-anki-vocab-cancel.$$"
: >"$CANCEL_OUT"

ndb() {
    [ -x "$QNDB" ] && "$QNDB" "$@" >/dev/null 2>>"$QNDB_LOG"
}

DIALOG_OPEN=false
CANCEL_PID=""

open_dialog() {
    ndb -m dlgConfirmCreate
    ndb -m dlgConfirmSetTitle "kobo-anki"
    ndb -m dlgConfirmSetBody "$1"
    ndb -m dlgConfirmSetReject "Cancel"
    ndb -m dlgConfirmSetModal true
    ndb -m dlgConfirmShowClose false
    ndb -m dlgConfirmShow

    # Without NickelDBus there's no dialog on screen and nothing to cancel -
    # skip the draw wait and the listener entirely rather than backgrounding
    # a call to a binary that isn't there. Everything below (the actual
    # lookup) still runs; it just runs silently.
    if [ -x "$QNDB" ]; then
        DIALOG_OPEN=true
        sleep 1 # let the first draw actually happen before anything else touches the screen

        # Listen for a Cancel tap for the rest of this run. 600s is far longer
        # than any realistic lookup; this just needs to outlive the run.
        "$QNDB" -s dlgConfirmResult -t 600000 >"$CANCEL_OUT" 2>>"$QNDB_LOG" &
        CANCEL_PID=$!
    fi
}

update_dialog() {
    ndb -m dlgConfirmSetBody "$1"
}

was_cancelled() {
    grep -q '0$' "$CANCEL_OUT" 2>/dev/null
}

cleanup() {
    if [ -n "$CANCEL_PID" ] && kill -0 "$CANCEL_PID" 2>/dev/null; then
        kill "$CANCEL_PID" 2>/dev/null
        wait "$CANCEL_PID" 2>/dev/null
    fi
    rm -f "$CANCEL_OUT"
    if [ "$DIALOG_OPEN" = "true" ] && ! was_cancelled; then
        ndb -m dlgConfirmClose
    fi
    DIALOG_OPEN=false
}
trap cleanup EXIT INT TERM

echo "=== start-vocab $(date) ===" >>$LOG

open_dialog "Checking for new words..."

NEW_COUNT=$(./bin/kobo-vocab -conf config/anki-mywords.conf -count-new 2>>$LOG)
case "$NEW_COUNT" in
    ''|*[!0-9]*) NEW_COUNT=0 ;; # unexpected output (crash, etc.) - fall back safely
esac

if was_cancelled; then
    echo "Cancelled by user" >>"$LOG"
    exit 1
fi

if [ "$NEW_COUNT" -eq 0 ]; then
    echo "No new words pending, skipping kobo-vocab" >>$LOG
    update_dialog "No new words found"
    sleep 3
    exit 0
fi

if [ "$NEW_COUNT" -eq 1 ]; then
    update_dialog "1 new word found, starting lookup..."
else
    update_dialog "$NEW_COUNT new words found, starting lookup..."
fi
sleep 1

# Run the real lookup in the background and mirror its own log output into
# the dialog body live, checking for a Cancel tap on every iteration.
SHOWN_LINES=$(wc -l <"$LOG" 2>/dev/null || echo 0)
./bin/kobo-vocab -conf config/anki-mywords.conf >>"$LOG" 2>&1 &
VOCAB_PID=$!

while kill -0 "$VOCAB_PID" 2>/dev/null; do
    if was_cancelled; then
        echo "Cancelled by user, stopping lookup" >>"$LOG"
        kill "$VOCAB_PID" 2>/dev/null
        wait "$VOCAB_PID" 2>/dev/null
        exit 1
    fi
    TOTAL_LINES=$(wc -l <"$LOG" 2>/dev/null || echo 0)
    if [ "$TOTAL_LINES" -gt "$SHOWN_LINES" ]; then
        LAST_LINE=$(sed -n "${TOTAL_LINES}p" "$LOG")
        [ -n "$LAST_LINE" ] && update_dialog "$LAST_LINE"
        SHOWN_LINES=$TOTAL_LINES
    fi
    sleep 1
done
wait "$VOCAB_PID"
RC=$?

if [ "$RC" -eq 0 ]; then
    update_dialog "Vocabulary updated"
else
    update_dialog "Vocabulary update failed (see log)"
fi
sleep 3

exit $RC
