#!/bin/bash
# Restart the concierge daemon onto the patched binary, resuming the current
# conversation. The point is the CLAUDE_CODE_CHILD_SESSION fix: the running
# daemon inherited that marker from the Claude Code shell that started h2, so
# transcript saving is off, which in turn blinds telegram-reply-guard (it reads
# the final assistant text out of the transcript). A daemon started by the
# patched binary strips the marker.
#
# Launched setsid-detached by concierge itself so it survives being stopped.
# --resume is tried first to keep the conversation; if that fails, fall back to
# a fresh role launch so the user is never left without a concierge. The
# bridge is never stopped — it is only re-pointed. bridge-watchdog (cron, every
# 2 min) is the final backstop.
export H2_DIR=/home/ubuntu/h2home
H2=/home/ubuntu/go/bin/h2
LOG=/home/ubuntu/h2home/logs/concierge-restart-resume.log
# shellcheck source=/home/ubuntu/h2home/bin/telegram-env.sh
. /home/ubuntu/h2home/bin/telegram-env.sh
BOT_TOKEN="$TELEGRAM_BOT_TOKEN"
CHAT_ID="$TELEGRAM_CHAT_ID"

# tg1.3 SPLIT (recovery script):
#  - alert()    = direct Bot API. Used ONLY for the "concierge did not come up"
#                 failure, which may coincide with a compound bridge+concierge
#                 outage, so it must not depend on the very path it is recovering.
#  - h2_alert() = informational, pod-is-up path. Routes through the h2 bridge so
#                 the message gets the HTML default and mirrors to concierge for
#                 the record. Safe here because it is only called after the
#                 concierge daemon is confirmed running (the bridge is never
#                 stopped by this script, only re-pointed).
alert() {
    curl -s -X POST "https://api.telegram.org/bot${BOT_TOKEN}/sendMessage" \
        -d chat_id="${CHAT_ID}" --data-urlencode "text=$1" >/dev/null
}

h2_alert() {
    local tmp
    tmp=$(mktemp /tmp/concierge-restart-alert.XXXXXX)
    printf '%s' "$1" > "$tmp"
    "$H2" send telegram --file "$tmp" >/dev/null 2>&1
    rm -f "$tmp"
}

marker_count() {
    local pid
    pid=$(pgrep -f "h2 _daemon --session-dir ${H2_DIR}/sessions/concierge" | head -1)
    [ -z "$pid" ] && { echo "no-daemon"; return; }
    tr '\0' '\n' < "/proc/$pid/environ" 2>/dev/null | grep -c "^CLAUDE_CODE_CHILD_SESSION="
}

{
    echo "==== $(date '+%Y-%m-%d %H:%M:%S') concierge restart+resume ===="
    echo "binary: $($H2 version 2>&1 | head -1)"
    echo "marker before: $(marker_count)"
    sleep 5   # let the launching turn finish and the call return

    echo "stopping concierge..."
    $H2 stop concierge 2>&1
    sleep 3

    echo "relaunching with --resume..."
    if $H2 run concierge --resume --detach 2>&1; then
        MODE=resume
    else
        echo "resume failed — falling back to a fresh role launch"
        $H2 run concierge --role concierge --detach 2>&1
        MODE=fresh
    fi
    sleep 5

    echo "re-pointing the telegram bridge..."
    $H2 bridge set-concierge concierge --bridge telegram 2>&1
    sleep 8

    echo "marker after: $(marker_count)"
    echo "---- pod state ----"
    $H2 list 2>&1

    if pgrep -f "h2 _daemon --session-dir ${H2_DIR}/sessions/concierge" >/dev/null; then
        # Pod is up → informational, route through the bridge (HTML + mirror).
        if [ "$(marker_count)" = "0" ]; then
            h2_alert "<b>Concierge restartet (${MODE}).</b> <code>CLAUDE_CODE_CHILD_SESSION</code> er borte — transkript skrives igjen og telegram-reply-guard har sikkerhetsnettet tilbake."
        else
            h2_alert "<b>Concierge restartet (${MODE}), MEN markøren henger fortsatt.</b> Sjekk <code>logs/concierge-restart-resume.log</code>."
        fi
    else
        # Concierge did not come up → possible compound outage → direct Bot API.
        alert "Concierge kom IKKE opp igjen etter restart. bridge-watchdog forsøker om et par minutter — se logs/concierge-restart-resume.log."
    fi
} >> "$LOG" 2>&1
