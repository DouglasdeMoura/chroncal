# chroncal

[![CI](https://github.com/DouglasdeMoura/chroncal/actions/workflows/ci.yml/badge.svg)](https://github.com/DouglasdeMoura/chroncal/actions/workflows/ci.yml)
[![Release](https://github.com/DouglasdeMoura/chroncal/actions/workflows/release.yml/badge.svg)](https://github.com/DouglasdeMoura/chroncal/actions/workflows/release.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/douglasdemoura/chroncal.svg)](https://pkg.go.dev/github.com/douglasdemoura/chroncal)
[![Go Report Card](https://goreportcard.com/badge/github.com/douglasdemoura/chroncal)](https://goreportcard.com/report/github.com/douglasdemoura/chroncal)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A terminal calendar backed by SQLite with full iCal import/export. Launch the TUI for an interactive calendar, or use the CLI for scriptable access to events, todos, alarms, and calendars.

Built for people who live in the terminal and want their calendar data local, portable, and standards-compliant.

## Features

- **Interactive TUI** with month, week, day, and agenda views
- **Full CLI** for scripting and automation
- **iCal import/export** with near-complete RFC 5545 coverage (VEVENT, VTODO, VALARM, VTIMEZONE)
- **Recurring events and todos** via RRULE, RDATE, and EXDATE
- **Alarm notifications** with desktop alerts, sound, and email
- **Multiple calendars** with color coding
- **Full-text search** across events and todos
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

# List upcoming events
chroncal event list --from 2026-04-01 --to 2026-04-30

# Search
chroncal event search "standup"

# Import from iCal
chroncal ical import calendar.ics --calendar Work

# Export to iCal
chroncal ical export --calendar Work -f work.ics
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

### Calendars

```
chroncal calendar list
chroncal calendar get     <id>
chroncal calendar create  "<name>" [--color HEX] [--description TEXT]
chroncal calendar update  <id> [--name NAME] [--color HEX] [--description TEXT]
chroncal calendar delete  <id>
```

### iCal import/export

```
chroncal ical import  <file.ics> [--calendar NAME]
chroncal ical export  [--calendar NAME] [--from DATE] [--to DATE] [--category TEXT] [--status TEXT] [-f FILE] [--events] [--todos]
```

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
- **VALARM**: 7/7 properties, plus RFC 9074 UID support
- **ATTENDEE/ORGANIZER**: all 11 parameters
- **VTIMEZONE**: round-trip preservation

Import from Google Calendar, Apple Calendar, Thunderbird, or any RFC 5545-compliant source. Export produces standards-compliant `.ics` files.

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
