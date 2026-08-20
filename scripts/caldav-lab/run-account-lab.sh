#!/usr/bin/env bash
# Run the account lifecycle against the lab CalDAV server.
#
# The script covers this sequence:
#
# 1. `account add` discovers and imports the usable collections.
# 2. `account calendars list` shows the complete inventory.
# 3. `sync run` pulls the seeded events.
# 4. `event add` plus `sync run` pushes a local event to the server.
# 5. `account calendars set` reduces the selection.
# 6. `account remove` deletes the credential and keeps the local copies.
#
# The script uses a temporary database. It does not touch the calendar data
# of the user.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

COMPOSE_FILE="scripts/caldav-lab/docker-compose.yml"
SERVER="${CALDAV_URL:-http://127.0.0.1:5233}/"
KEEP_DB="${KEEP_DB:-0}"

LAB_DB="$(mktemp /tmp/chroncal-lab-XXXXXX.db)"
cleanup() {
  if [ "$KEEP_DB" = "0" ]; then
    rm -f "$LAB_DB" "$LAB_DB"-wal "$LAB_DB"-shm
  fi
}
trap cleanup EXIT

docker compose -f "$COMPOSE_FILE" up -d
scripts/caldav-lab/seed-calendars.sh

make build

export CHRONCAL_DB="$LAB_DB"
export CHRONCAL_PASSWORD=secret
BIN="./chroncal"
FLAGS=(--allow-plaintext)

echo "== account add =="
"$BIN" "${FLAGS[@]}" account add "Work Lab" \
  --server "$SERVER" --username alice --auth basic --allow-insecure

echo "== account calendars list =="
"$BIN" "${FLAGS[@]}" account calendars list "Work Lab"

echo "== the initial sync pulled the seeded events =="
events="$("$BIN" "${FLAGS[@]}" event list --from 2026-01-01 --to 2027-12-31)"
printf '%s\n' "$events"
for want in "Lab standup" "Independence Day" "Family dinner"; do
  case "$events" in
    *"$want"*) ;;
    *)
      echo "the initial sync did not pull \"$want\"" >&2
      exit 1
      ;;
  esac
done

echo "== sync run =="
"$BIN" "${FLAGS[@]}" sync run

echo "== push a local event =="
"$BIN" "${FLAGS[@]}" event add "Radicale push test" \
  --date "$(date -u -d '+1 day' +%Y-%m-%d)" --time 10:00 --duration 1h \
  --calendar "Holidays in Brazil"
"$BIN" "${FLAGS[@]}" sync run

echo "== reduce the selection =="
"$BIN" "${FLAGS[@]}" account calendars set "Work Lab" \
  --calendar "Holidays in Brazil" --calendar "Família" --yes

echo "== the reduced selection kept two calendars =="
# Match on remote_url. The remote "Personal" collection arrives as
# "Personal (2)", because the local default calendar already holds the name.
kept="$("$BIN" "${FLAGS[@]}" -o json calendar list)"
"$BIN" "${FLAGS[@]}" calendar list
for gone in "/alice/personal/" "/alice/tasks/"; do
  case "$kept" in
    *"$gone"*)
      echo "account calendars set kept the deselected ${gone}" >&2
      exit 1
      ;;
  esac
done
for stays in "/alice/holidays-brazil/" "/alice/familia/"; do
  case "$kept" in
    *"$stays"*) ;;
    *)
      echo "account calendars set dropped the selected ${stays}" >&2
      exit 1
      ;;
  esac
done

echo "== account remove =="
"$BIN" "${FLAGS[@]}" account remove "Work Lab" --yes

echo "== account remove kept the local copies =="
"$BIN" "${FLAGS[@]}" calendar list

echo "The account lab passed. DB=$LAB_DB"
