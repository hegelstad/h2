#!/bin/bash
# One-shot: restart ONLY the concierge daemon onto the freshly-installed
# combined binary (GitRef 70811f6 = full stack + grok + h2-wkg fix), then
# re-point the still-running telegram bridge at the new concierge session.
# The bridge itself is NOT stopped (it already runs the full stack; the h2-wkg
# fix is agent-side only) so there is no bridge downtime.
# Launched setsid-detached by concierge itself so it survives concierge being
# stopped. The bridge-watchdog cron is the backstop if anything fails.
export H2_DIR=/home/ubuntu/h2home
H2=/home/ubuntu/go/bin/h2
LOG=/home/ubuntu/h2home/logs/concierge-redeploy.log
# shellcheck source=/home/ubuntu/h2home/bin/telegram-env.sh
. /home/ubuntu/h2home/bin/telegram-env.sh
BOT_TOKEN="$TELEGRAM_BOT_TOKEN"
CHAT_ID="$TELEGRAM_CHAT_ID"

{
  echo "==== $(date '+%Y-%m-%d %H:%M:%S') concierge-redeploy start; binary: $($H2 version 2>&1 | head -1) ===="
  sleep 5   # let concierge's current turn finish and the launching call return
  echo "stopping concierge (old binary)..."
  $H2 stop concierge 2>&1
  sleep 3
  echo "relaunching concierge on new binary..."
  $H2 run concierge --role concierge --detach 2>&1
  sleep 2
  echo "re-pointing running telegram bridge at new concierge..."
  $H2 bridge set-concierge concierge --bridge telegram 2>&1
  sleep 8
  echo "---- post-redeploy pod state ----"
  $H2 list 2>&1
} >> "$LOG" 2>&1

# tg1.3 SPLIT (recovery script): the bridge is never stopped here, only
# re-pointed, so on success both pod and bridge are confirmed up and the
# confirmation is routed through the h2 bridge (HTML default + mirror to
# concierge). On failure — a possible compound bridge+concierge outage — the
# alert must NOT depend on the path it is recovering, so it goes direct Bot API.
sleep 2
CONC_UP=$(pgrep -f '_daemon --session-dir /home/ubuntu/h2home/sessions/concierge' | head -1)
BR_UP=$(pgrep -f 'h2 _bridge-service --bridge telegram' | head -1)
if [ -n "$CONC_UP" ] && [ -n "$BR_UP" ]; then
  MSG="<b>Deploy fullfoert.</b> Hele poden kjoerer naa paa den kombinerte binaeren (GitRef 70811f6): hele stacken (--format/HTML, rich, media, rate-limit, grok) PLUSS h2-wkg-fiksen. concierge er restartet paa ny binaer og broen peker paa den. coder-1, reviewer og scheduler kjoerer ogsaa paa ny binaer. coder-2 er fortsatt nede (gammel worktree, ikke kritisk). Send meg en melding saa bekrefter jeg at ruting virker."
  TMP=$(mktemp /tmp/concierge-redeploy-alert.XXXXXX)
  printf '%s' "$MSG" > "$TMP"
  "$H2" send telegram --file "$TMP" >/dev/null 2>&1
  rm -f "$TMP"
else
  MSG="Deploy: concierge-restart kjoert men verifisering feilet (concierge_up=${CONC_UP:-none} bridge_up=${BR_UP:-none}). Sjekk ~/h2home/logs/concierge-redeploy.log. bridge-watchdog forsoeker gjenoppretting innen 2 min."
  curl -s -X POST "https://api.telegram.org/bot${BOT_TOKEN}/sendMessage" \
    -d chat_id="${CHAT_ID}" --data-urlencode "text=${MSG}" >/dev/null
fi
echo "$(date '+%Y-%m-%d %H:%M:%S') redeploy script done (concierge=${CONC_UP:-none} bridge=${BR_UP:-none})" >> "$LOG"
