# chroncal

[![CI](https://github.com/DouglasdeMoura/chroncal/actions/workflows/ci.yml/badge.svg)](https://github.com/DouglasdeMoura/chroncal/actions/workflows/ci.yml)
[![Release](https://github.com/DouglasdeMoura/chroncal/actions/workflows/release.yml/badge.svg)](https://github.com/DouglasdeMoura/chroncal/actions/workflows/release.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/douglasdemoura/chroncal.svg)](https://pkg.go.dev/github.com/douglasdemoura/chroncal)
[![Go Report Card](https://goreportcard.com/badge/github.com/douglasdemoura/chroncal)](https://goreportcard.com/report/github.com/douglasdemoura/chroncal)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A terminal calendar backed by SQLite with full iCal import/export and CalDAV sync. Launch the TUI for an interactive calendar, or use the CLI for scriptable access to events, todos, journals, alarms, free/busy queries, and calendars.

Built for people who live in the terminal and want their calendar data local, portable, and standards-compliant.

## Features

- **Interactive TUI** with month, week, day, and agenda views
- **Full CLI** for scripting and automation
- **iCal import/export** with broad RFC 5545 coverage (VEVENT, VTODO, VJOURNAL, VALARM, VTIMEZONE)
- **CalDAV sync** with account discovery, linked calendars, conflict handling, and sync status
- **Free/busy queries** from local data or remote CalDAV `VFREEBUSY` reports
- **Recurring events and todos** via RRULE, RDATE, and EXDATE
- **Recurring journals** via RRULE, RDATE, and EXDATE
- **Alarm notifications** with desktop alerts, sound, and email
- **Multiple calendars** with color coding
- **Full-text search** across events, todos, and journals
- **Attendees, attachments, comments, contacts, resources, and relations**
- **SQLite storage** with automatic migrations
- **Cross-platform** (Linux, macOS, Windows)
- **Four output formats**: text, table, JSON, YAML

## Installation

### From source

Requires [Go](https://go.dev/) 1.25+.

```bash
go install github.com/douglasdemoura/chroncal/cmd/chroncal@latest
```

### Build locally

```bash
git clone https://github.com/DouglasdeMoura/chroncal.git
cd chroncal
make build
./chroncal
```

## Quick start

```bash
# Launch the interactive TUI
chroncal

# Create a calendar
chroncal calendar create "Work" --color "#3B82F6"

# Add an event
chroncal event add "Team standup" --date 2026-04-01 --time 09:00 --duration 30m --calendar Work

# Add a recurring event
chroncal event add "Weekly review" --date 2026-04-04 --time 14:00 --duration 1h --rrule "FREQ=WEEKLY;BYDAY=FR"

# Add a todo
chroncal todo add "Write quarterly report" --due 2026-04-15 --priority 1

# Add a journal entry
chroncal journal add "Weekly notes" --date 2026-04-04 --calendar Work

# List upcoming events
chroncal event list --from 2026-04-01 --to 2026-04-30

# Search
chroncal event search "standup"

# Import from iCal
chroncal ical import calendar.ics --calendar Work

# Export to iCal
chroncal ical export --calendar Work -f work.ics

# Discover and link a remote CalDAV calendar after adding an account
chroncal account discover "Google Work"
chroncal calendar link "Work" --account "Google Work" --remote-url /remote/calendar/path/

# Run sync and inspect status
chroncal sync run --calendar Work
chroncal sync status

# Compute local free/busy for a range
chroncal freebusy --calendar Work --from 2026-04-01 --to 2026-04-30
```

## CLI reference

### Events

```
chroncal event list    [--from DATE] [--to DATE] [--calendar NAME] [--status STATUS]
chroncal event get     <id|uid> [--recurrence-id ID]
chroncal event search  <query> [--calendar NAME] [--from DATE] [--to DATE] [--status STATUS]
chroncal event add     "<title>" [flags]
chroncal event update  <id|uid> [flags] [--recurrence-id ID]
chroncal event delete  <id|uid> [--recurrence-id ID]
```

Event flags: `--date`, `--time`, `--end-time`, `--duration`, `--timezone`, `--location`, `--description`, `--calendar`, `--status`, `--class`, `--transparency`, `--priority`, `--url`, `--categories`, `--geo`, `--rrule`, `--exdate`, `--rdate`, `--attach`, `--alarm`, `--attendee`, `--organizer`, `--contact`, `--resource`, `--comment`, `--related-to`

### Todos

```
chroncal todo list      [--calendar NAME] [--status STATUS] [--all] [--from DATE] [--to DATE]
chroncal todo get       <id|uid> [--recurrence-id ID]
chroncal todo search    <query> [--calendar NAME] [--status STATUS] [--completed] [--incomplete]
chroncal todo add       "<summary>" [flags]
chroncal todo update    <id|uid> [flags] [--recurrence-id ID]
chroncal todo complete  <id|uid> [--recurrence-id ID]
chroncal todo delete    <id|uid> [--recurrence-id ID]
```

Todo flags: `--due`, `--start`, `--duration`, `--location`, `--description`, `--calendar`, `--status`, `--progress`, `--class`, `--priority`, `--url`, `--categories`, `--geo`, `--rrule`, `--exdate`, `--rdate`, `--attach`, `--alarm`, `--attendee`, `--organizer`, `--contact`, `--resource`, `--comment`, `--related-to`

### Journals

```
chroncal journal list    [--from DATE] [--to DATE] [--calendar NAME] [--status STATUS] [--all]
chroncal journal get     <id|uid> [--recurrence-id ID]
chroncal journal search  <query> [--calendar NAME] [--from DATE] [--to DATE] [--status STATUS]
chroncal journal add     "<summary>" [flags]
chroncal journal update  <id|uid> [flags] [--recurrence-id ID]
chroncal journal delete  <id|uid> [--recurrence-id ID]
```

Journal flags: `--date`, `--description`, `--calendar`, `--status`, `--class`, `--url`, `--categories`, `--rrule`, `--exdate`, `--rdate`, `--attach`, `--attendee`, `--organizer`, `--contact`, `--comment`, `--related-to`

### Calendars

```
chroncal calendar list
chroncal calendar get     <id>
chroncal calendar create  "<name>" [--color HEX] [--description TEXT]
chroncal calendar link    <id|name> --account <id|name> --remote-url <href>
chroncal calendar update  <id> [--name NAME] [--color HEX] [--description TEXT]
chroncal calendar unlink  <id|name>
chroncal calendar delete  <id>
```

### iCal import/export

```
chroncal ical import  <file.ics> [--calendar NAME]
chroncal ical export  [--calendar NAME] [--from DATE] [--to DATE] [--category TEXT] [--status TEXT] [-f FILE] [--events] [--todos] [--journals]
```

### CalDAV accounts and sync

```
chroncal account add       "<name>" [flags]
chroncal account discover  <name|id>
chroncal account list
chroncal account remove    <name|id>

chroncal sync run          [--calendar NAME] [--conflict MODE]
chroncal sync status
chroncal sync conflicts
chroncal sync resolve      <id> --pick {local,server}
chroncal sync reset        [--calendar NAME]
```

`chroncal account discover` lists remote calendars and their supported component sets. Link a local calendar to one of those remote hrefs with `chroncal calendar link` before running sync.

### Google Calendar via CalDAV

Google Calendar requires OAuth 2.0 and only exposes `VEVENT` over CalDAV.

Account credentials are stored in the OS keyring by default. Only use
`--allow-plaintext` on systems where no keyring backend is available and you
accept the local at-rest risk.

1. Create a desktop OAuth client in the [Google Cloud Console](https://console.cloud.google.com/).
2. Enable the Google Calendar API for that project.
3. Add the account:

```bash
chroncal account add "Google Work" \
  --server https://apidata.googleusercontent.com/caldav/v2 \
  --auth oauth2 \
  --oauth-client-id "YOUR_CLIENT_ID.apps.googleusercontent.com"
```

4. Discover remote calendars and link one to a local calendar:

```bash
chroncal account discover "Google Work"
chroncal calendar create "Work"
chroncal calendar link "Work" \
  --account "Google Work" \
  --remote-url /caldav/v2/YOUR_CALENDAR_ID/events/
```

5. Run sync and inspect status:

```bash
chroncal sync run --calendar "Work"
chroncal sync status
```

Google limitations:

- Google CalDAV only supports `VEVENT`. Use Nextcloud, Radicale, or Fastmail for `VTODO` and `VJOURNAL`.
- Google does not support `sync-collection` REPORT, so chroncal falls back to ctag + ETag comparison.

### Free/busy

```
chroncal freebusy --calendar NAME --from DATE_OR_RFC3339 --to DATE_OR_RFC3339 [--remote] [--format {text,ical}]
```

Without `--remote`, `freebusy` computes busy time from local recurring data. With `--remote`, it sends a CalDAV free-busy report to the linked remote calendar.

### Alarms

```
chroncal alarm check                          # Fire due alarms (one-shot)
chroncal alarm list                           # List unacknowledged alarms
chroncal alarm dismiss  <state-id>            # Dismiss a fired alarm
chroncal alarm snooze   <state-id> [--for DURATION] [--until-start]
chroncal alarm daemon   [--interval DURATION] # Run alarm checks in a loop (default: 30s)
chroncal alarm missed   [--days N]            # Show missed alarms (default lookback: 7 days)
```

### Service (alarm background service)

```
chroncal service install    # Install systemd timer (Linux) or launchd agent (macOS)
chroncal service run        # Run one background-service cycle now
chroncal service uninstall
chroncal service status
```

### Global flags

All commands accept `-o, --output {text,table,json,yaml}` (default: text).

## TUI

Run `chroncal` with no arguments to launch the interactive terminal interface.

**Views**: month, week, day, agenda. Switch with `m`, `w`, `d`, `a`.

The TUI supports creating and editing events and todos, browsing calendars in a sidebar, and viewing full event/todo details including alarms, attendees, and attachments.

## Configuration

Configuration is loaded in order of precedence:

1. **Environment variables** (prefix `CHRONCAL_`, e.g., `CHRONCAL_DB`)
2. **Config file** at `$XDG_CONFIG_HOME/chroncal/config.toml` (or `~/.config/chroncal/config.toml`)
3. **Defaults**

### Config keys

| Key | Description | Default |
|-----|-------------|---------|
| `db` | Path to SQLite database | `$XDG_DATA_HOME/chroncal/chroncal.db` |
| `nerd_fonts` | Enable Nerd Font icons in TUI | `false` |
| `product_id` | iCal PRODID for export | `-//chroncal//chroncal//EN` |

### SMTP (for email alarms)

Configure via environment variables or `config.toml`:

```toml
[smtp]
host = "smtp.example.com"
port = 587
username = "you@example.com"
password = "app-password"
from = "you@example.com"
```

Or via environment: `CHRONCAL_SMTP_HOST`, `CHRONCAL_SMTP_PORT`, `CHRONCAL_SMTP_USERNAME`, `CHRONCAL_SMTP_PASSWORD`, `CHRONCAL_SMTP_FROM`.

### Desktop notification backends

`chroncal alarm check` records fired alarms even in headless environments, but
`DISPLAY` and `AUDIO` notifications still need an OS notification backend.
On minimal containers or SSH sessions without desktop tooling, notification
delivery can fail even though the alarm is detected and listed by
`chroncal alarm list`.

## Data storage

The database is a single SQLite file:

- **Linux**: `~/.local/share/chroncal/chroncal.db`
- **macOS**: `~/Library/Application Support/chroncal/chroncal.db`

Override with `CHRONCAL_DB` or the `db` config key.

Migrations run automatically on startup. WAL mode is enabled for better concurrency.

## iCal compatibility

chroncal aims for complete RFC 5545 compliance. Current coverage:

- **VEVENT**: 30/31 properties (RSTATUS excluded, iTIP-only)
- **VTODO**: 31/32 properties (RSTATUS excluded, iTIP-only)
- **VJOURNAL**: core component import/export and CalDAV sync support
- **VALARM**: 7/7 properties, plus RFC 9074 UID support
- **ATTENDEE/ORGANIZER**: all 11 parameters
- **VTIMEZONE**: round-trip preservation
- **VFREEBUSY**: local compute/export plus remote CalDAV query support

Import from Google Calendar, Apple Calendar, Thunderbird, or any RFC 5545-compliant source. Export produces standards-compliant `.ics` files.

## CalDAV interoperability

Live interoperability QA has been run against Nextcloud CalDAV with:

- `VEVENT`: create, update, delete, recurrence, timezone, conflict handling
- `VTODO`: create, update, delete, recurrence, duration/start semantics, conflict handling
- `VJOURNAL`: create, update, delete, recurrence, conflict handling
- `VALARM`: round-trip sync on `VEVENT` and `VTODO`, including repeated alarms

Nextcloud does not expose a `VJOURNAL` collection by default, but chroncal
interoperates cleanly with a dedicated CalDAV calendar created with
`supported-calendar-component-set = VJOURNAL`.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, testing, and code conventions.

## Links

- [GitHub Repository](https://github.com/DouglasdeMoura/chroncal)
- [Go Package Reference](https://pkg.go.dev/github.com/douglasdemoura/chroncal)
- [Issue Tracker](https://github.com/DouglasdeMoura/chroncal/issues)
- [Releases](https://github.com/DouglasdeMoura/chroncal/releases)
- [Contributing Guide](CONTRIBUTING.md)
- [Security Policy](SECURITY.md)

## License

[MIT](LICENSE) - Douglas de Moura
