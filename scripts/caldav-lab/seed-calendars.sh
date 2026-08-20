#!/usr/bin/env bash
# Create the lab collections on the Radicale server.
#
# The set covers a VEVENT collection, a VTODO collection, a VFREEBUSY
# collection, and a non-ASCII display name. Chroncal discovery must
# separate the usable collections from the unusable ones.
set -euo pipefail

BASE_URL="${CALDAV_URL:-http://127.0.0.1:5233}"
USER="${CALDAV_USER:-alice}"
PASS="${CALDAV_PASS:-secret}"
AUTH="${USER}:${PASS}"

mkcalendar() {
  local path="$1" display="$2" components="$3" color="${4:-#4285f4}"
  local status
  status=$(curl -sS -u "$AUTH" -o /dev/null -w '%{http_code}' -X MKCALENDAR \
    -H 'Content-Type: application/xml; charset=utf-8' --data-binary @- "${BASE_URL}${path}" <<XML
<?xml version="1.0" encoding="utf-8"?>
<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:ICAL="http://apple.com/ns/ical/">
  <D:set><D:prop>
    <D:displayname>${display}</D:displayname>
    <C:supported-calendar-component-set>${components}</C:supported-calendar-component-set>
    <ICAL:calendar-color>${color}</ICAL:calendar-color>
  </D:prop></D:set>
</C:mkcalendar>
XML
)
  # 405 and 409 mean the collection is already present. The script is idempotent.
  case "$status" in
    201 | 405 | 409) ;;
    *)
      echo "mkcalendar ${path} failed with HTTP ${status}" >&2
      return 1
      ;;
  esac
}

put_event() {
  local path="$1" uid="$2" summary="$3"
  local status
  status=$(curl -sS -u "$AUTH" -o /dev/null -w '%{http_code}' -X PUT \
    -H 'Content-Type: text/calendar; charset=utf-8' --data-binary @- "${BASE_URL}${path}" <<ICS
BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Chroncal Lab//EN
BEGIN:VEVENT
UID:${uid}
DTSTAMP:20260721T120000Z
DTSTART:20260722T140000Z
DTEND:20260722T150000Z
SUMMARY:${summary}
END:VEVENT
END:VCALENDAR
ICS
)
  case "$status" in
    201 | 204 | 409 | 412) ;;
    *)
      echo "put_event ${path} failed with HTTP ${status}" >&2
      return 1
      ;;
  esac
}

# Wait a maximum of 15 seconds for the server.
for _ in $(seq 1 30); do
  curl -fsS -u "$AUTH" "${BASE_URL}/" > /dev/null 2>&1 && break
  sleep 0.5
done

mkcalendar "/${USER}/personal/" "Personal" '<C:comp name="VEVENT"/>' '#112233'
mkcalendar "/${USER}/holidays-brazil/" "Holidays in Brazil" '<C:comp name="VEVENT"/>' '#445566'
mkcalendar "/${USER}/familia/" "Família" '<C:comp name="VEVENT"/>' '#228B22'
mkcalendar "/${USER}/tasks/" "Tasks" '<C:comp name="VTODO"/>' '#AA5500'
mkcalendar "/${USER}/availability/" "Availability" '<C:comp name="VFREEBUSY"/>' '#778899'

put_event "/${USER}/personal/lab-meeting.ics" "lab-personal-1" "Lab standup"
put_event "/${USER}/holidays-brazil/independence-day.ics" "lab-holiday-1" "Independence Day"
put_event "/${USER}/familia/dinner.ics" "lab-family-1" "Family dinner"

echo "Seeded the collections for ${USER} at ${BASE_URL}"
