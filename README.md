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
- **CalDAV sync** with per-calendar remote connections, conflict handling, at-a-glance sync health, and in-app Google re-authentication
- **Free/busy queries** from local data or remote CalDAV `VFREEBUSY` reports
- **Recurring events and todos** via RRULE, RDATE, and EXDATE
- **Recurring journals** via RRULE, RDATE, and EXDATE
- **Alarm notifications** with desktop alerts, sound, and email
- **Multiple calendars** with color coding
- **Full-text search** across events, todos, and journals
- **Attendees, attachments, comments, contacts, resources, and relations**
- **SQLite storage** with automatic migrations
- **Cross-platform** (Linux, macOS, Windows)
- **Two output formats**: text for humans, JSON for scripts and LLMs

## Installation

| Method | Platforms | Best for |
| --- | --- | --- |
| Install script | Linux, macOS, FreeBSD, OpenBSD | Prebuilt binary users who want one command |
| Homebrew | macOS, Linux | Managed installs and upgrades |
| Go install | Any platform with Go 1.25+ | Go users and contributors |
| mise | macOS, Linux | Users who already manage tools with mise |
| Nix | Linux, macOS | `nix run` and profile installs |
| Scoop | Windows | Managed Windows installs |
| AUR | Arch Linux | `yay`/`paru` users (binary or source package) |
| Build from source | Any platform with Go 1.25+ | Contributors and packagers |

### Install script (Linux / macOS / BSD)

No Go toolchain required. The installer detects your OS and architecture,
downloads the latest release archive, verifies it against `checksums.txt`, and
installs `chroncal` to `/usr/local/bin` when possible. If `sudo` is unavailable,
it falls back to `~/.local/bin`.

```bash
curl -fsSL https://raw.githubusercontent.com/DouglasdeMoura/chroncal/master/scripts/install.sh | sh
```

Pin an exact version:

```bash
curl -fsSL https://raw.githubusercontent.com/DouglasdeMoura/chroncal/master/scripts/install.sh | env VERSION=v0.2.3 sh
```

Install somewhere else:

```bash
curl -fsSL https://raw.githubusercontent.com/DouglasdeMoura/chroncal/master/scripts/install.sh | env INSTALL_DIR="$HOME/.local/bin" sh
```

If your environment cannot run checksum tools, you can opt out with
`VERIFY_CHECKSUM=0`, but checksum verification is recommended.

### Homebrew (macOS / Linux)

```bash
brew tap douglasdemoura/tap && brew install chroncal
```

Upgrade:

```bash
brew update && brew upgrade chroncal
```

Uninstall:

```bash
brew uninstall chroncal
```

GoReleaser pushes the formula to `DouglasdeMoura/homebrew-tap` automatically
on each release (when the `HOMEBREW_TAP_TOKEN` repository secret is
configured). If Homebrew is temporarily unavailable for a new release, use
the install script, mise, Nix, or `go install`.

### Go install

Requires [Go](https://go.dev/) 1.25+.

```bash
go install github.com/douglasdemoura/chroncal/cmd/chroncal@latest
```

Pin an exact release:

```bash
go install github.com/douglasdemoura/chroncal/cmd/chroncal@v0.2.3
```

The binary is usually installed to `$(go env GOPATH)/bin/chroncal`. Make sure
that directory is on your `PATH`.

### mise

Install the latest GitHub release globally:

```bash
mise use -g github:DouglasdeMoura/chroncal
```

Pin an exact release globally:

```bash
mise use -g github:DouglasdeMoura/chroncal@0.2.3
```

If a just-published release does not appear yet, clear mise's GitHub release
cache first:

```bash
mise cache clear
mise ls-remote github:DouglasdeMoura/chroncal
```

### Nix

Run without installing:

```bash
nix run github:DouglasdeMoura/chroncal
```

Install to your profile:

```bash
nix profile install github:DouglasdeMoura/chroncal
```

Build the package from a clone:

```bash
nix build .#chroncal
```

The flake exposes `packages.default`, `packages.chroncal`, `apps.default`, and a
developer shell with Go, GoReleaser, golangci-lint, govulncheck, and sqlc.

### Scoop (Windows)

Windows users can install with:

```powershell
scoop bucket add chroncal https://github.com/DouglasdeMoura/scoop-bucket
scoop install chroncal
```

Upgrade:

```powershell
scoop update chroncal
```

The manifest is generated by GoReleaser and pushed to
`DouglasdeMoura/scoop-bucket` on each release.

### Arch Linux AUR

Two AUR packages are published:

```bash
yay -S chroncal-bin  # prebuilt Linux binary from GitHub Releases
yay -S chroncal      # builds from source with your local Go toolchain
```

`chroncal-bin` is fastest for x86_64 and aarch64 users. `chroncal` is the right
choice when you want to build locally or use another Arch-supported CPU target.
Both packages are generated by GoReleaser (`aurs` and `aur_sources` in
`.goreleaser.yml`) and pushed to the AUR automatically on each release.

### Build from source

```bash
git clone https://github.com/DouglasdeMoura/chroncal.git
cd chroncal
make build
./chroncal version
```

Run the test suite before sending changes:

```bash
go test ./...
```

### Maintainer checklist

Before cutting a release:

1. Make sure CI is green on `master`.
2. Bump the `VERSION` file to the new version (no leading `v`) — the release
   workflow refuses to run if it does not match the tag.
3. Run `goreleaser check` locally if GoReleaser is installed. Exit code 2
   with a `brews` deprecation warning is expected (casks are macOS-only, so
   the formula publisher is kept deliberately); only exit code 1 means the
   config is broken.
4. Create a `v*` tag and push it.
5. Confirm the GitHub Release includes archives, `checksums.txt`, and install snippets.
6. Confirm the install script works for the new tag.
7. Confirm `brew tap douglasdemoura/tap && brew install chroncal` works after the Homebrew tap update.
8. Confirm `scoop update chroncal` sees the new Scoop manifest.
9. Confirm the AUR packages were pushed: <https://aur.archlinux.org/packages/chroncal> and <https://aur.archlinux.org/packages/chroncal-bin>.
10. Confirm `go install github.com/douglasdemoura/chroncal/cmd/chroncal@<tag>` works.
11. Confirm `mise use -g github:DouglasdeMoura/chroncal@<version>` resolves the release.

If a release run fails after some assets were already uploaded, do not just
rerun it: GoReleaser refuses to overwrite existing release assets and the
rerun dies with `already_exists` before reaching the package publishers.
Delete the assets first, then rerun the failed job — the release object and
its notes are kept, and all channels get hashes consistent with the fresh
assets:

```bash
for a in $(gh release view vX.Y.Z --json assets --jq '.assets[].name'); do
  gh release delete-asset vX.Y.Z "$a" -y
done
gh run rerun <run-id> --failed
```

Required repository secrets:

| Secret | Purpose | Required |
| --- | --- | --- |
| `GITHUB_TOKEN` | Created automatically by GitHub Actions; publishes release assets | Yes |
| `HOMEBREW_TAP_TOKEN` | Personal access token with write access to `DouglasdeMoura/homebrew-tap` | No, but Homebrew updates are skipped without it |
| `SCOOP_BUCKET_TOKEN` | Personal access token with write access to `DouglasdeMoura/scoop-bucket` | No, but Scoop updates fall back to `HOMEBREW_TAP_TOKEN` if it has access |
| `AUR_KEY` | Unencrypted SSH private key registered to the AUR maintainer account | No, but AUR publishing is skipped without it |

The flake reads its version from the `VERSION` file, so Nix needs no
per-release edits. When `go.mod` or `go.sum` changes, the flake's `vendorHash`
must change with it — the Nix CI workflow builds the flake on any PR touching
those files and fails on a mismatch. To fix one, run `nix build .#chroncal`,
copy the `got:` hash into `flake.nix`, and rerun the build. A monthly
`update-flake-lock` workflow opens a PR refreshing the flake inputs.

GoReleaser publishes everything in one run — release assets, Homebrew
formula, Scoop manifest, and both AUR packages; no manual packaging steps
remain.

Future package channel: `.deb` and `.rpm` assets can be added later with
GoReleaser nFPM once the primary package manager channels are stable.

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

# Connect a local calendar to a remote CalDAV URL at create time
chroncal calendar create "Work" --remote-url https://cal.example.com/dav/calendars/work/ \
    --username alice --auth basic

# Run sync and inspect status
chroncal sync run --calendar Work
chroncal sync status

# Compute local free/busy for a range
chroncal freebusy --calendar Work --from 2026-04-01 --to 2026-04-30
```

## CLI reference

### Events

```
chroncal event list           [--from DATE] [--to DATE] [--calendar NAME] [--status STATUS] [--include-deleted]
chroncal event get            <id|uid> [--recurrence-id ID]
chroncal event search         <query> [--calendar NAME] [--from DATE] [--to DATE] [--status STATUS]
chroncal event add            "<title>" [flags]
chroncal event update         <id|uid> [flags] [--recurrence-id ID]
chroncal event delete         <id|uid> [--recurrence-id ID] [--yes]
chroncal event restore        <id|uid>
chroncal event purge          <id> [--yes]
chroncal event purge-deleted  [--older-than DURATION] [--yes]
```

Event flags: `--date`, `--time`, `--end-time`, `--duration`, `--timezone`, `--location`, `--description`, `--calendar`, `--status`, `--class`, `--transparency`, `--priority`, `--url`, `--categories`, `--geo`, `--rrule`, `--exdate`, `--rdate`, `--attach`, `--alarm`, `--attendee`, `--organizer`, `--contact`, `--resource`, `--comment`, `--related-to`

### Todos

```
chroncal todo list           [--calendar NAME] [--status STATUS] [--all] [--from DATE] [--to DATE] [--include-deleted]
chroncal todo get            <id|uid> [--recurrence-id ID]
chroncal todo search         <query> [--calendar NAME] [--status STATUS] [--completed] [--incomplete]
chroncal todo add            "<summary>" [flags]
chroncal todo update         <id|uid> [flags] [--recurrence-id ID]
chroncal todo complete       <id|uid> [--recurrence-id ID]
chroncal todo delete         <id|uid> [--recurrence-id ID] [--yes]
chroncal todo restore        <id|uid>
chroncal todo purge          <id> [--yes]
chroncal todo purge-deleted  [--older-than DURATION] [--yes]
```

Todo flags: `--due`, `--start`, `--duration`, `--location`, `--description`, `--calendar`, `--status`, `--progress`, `--class`, `--priority`, `--url`, `--categories`, `--geo`, `--rrule`, `--exdate`, `--rdate`, `--attach`, `--alarm`, `--attendee`, `--organizer`, `--contact`, `--resource`, `--comment`, `--related-to`

### Journals

```
chroncal journal list           [--from DATE] [--to DATE] [--calendar NAME] [--status STATUS] [--all] [--include-deleted]
chroncal journal get            <id|uid> [--recurrence-id ID]
chroncal journal search         <query> [--calendar NAME] [--from DATE] [--to DATE] [--status STATUS]
chroncal journal add            "<summary>" [flags]
chroncal journal update         <id|uid> [flags] [--recurrence-id ID]
chroncal journal delete         <id|uid> [--recurrence-id ID] [--yes]
chroncal journal restore        <id|uid>
chroncal journal purge          <id> [--yes]
chroncal journal purge-deleted  [--older-than DURATION] [--yes]
```

Journal flags: `--date`, `--description`, `--calendar`, `--status`, `--class`, `--url`, `--categories`, `--rrule`, `--exdate`, `--rdate`, `--attach`, `--attendee`, `--organizer`, `--contact`, `--comment`, `--related-to`

### Calendars

```
chroncal calendar list
chroncal calendar get     <id>
chroncal calendar create  "<name>" [--color HEX] [--description TEXT] [--email ADDR] [remote flags]
chroncal calendar update  <id|name> [--name NAME] [--color HEX] [--description TEXT] [--email ADDR] [remote flags] [--disconnect-remote]
chroncal calendar delete  <id>
```

Remote flags (used with `create` or `update` to attach a CalDAV URL directly
to a calendar — there is no separate `account` concept):

```
--remote-url <href>
--username <user>
--auth {basic,bearer,oauth2}
--oauth-client-id <id>
--allow-insecure
```

Pass `--disconnect-remote` on `update` to remove a calendar's remote link.

### iCal import/export

```
chroncal ical import  <file.ics> [--calendar NAME]
chroncal ical export  [--calendar NAME] [--from DATE] [--to DATE] [--category TEXT] [--status TEXT] [-f FILE] [--events] [--todos] [--journals]
```

Imports are bounded to reduce resource exhaustion from untrusted calendar
data. `chroncal ical import` rejects `.ics` payloads larger than 8 MiB and
inline base64 attachments larger than 1 MiB decoded.

### Sync

```
chroncal sync run       [--calendar NAME] [--conflict MODE]
chroncal sync status
chroncal sync conflicts
chroncal sync resolve   <id> --pick {local,server}
chroncal sync reset     [--calendar NAME]
```

Sync operates on each connected calendar independently. To connect a local
calendar to a remote CalDAV URL, use the remote flags on `calendar create` or
`calendar update` (see above).

### Google Calendar via CalDAV

Google Calendar requires OAuth 2.0 and only exposes `VEVENT` over CalDAV.

Credentials are stored in the OS keyring by default. chroncal uses OAuth PKCE
for installed-app flows, but Google's token endpoint also requires the Desktop
client's `client_secret` even with PKCE — so both the client ID *and* the
client secret are needed at setup time. Refresh tokens (and the client secret)
are persisted to the keyring after the first authorization, so subsequent
syncs run unattended.

> **Plaintext fallback caveat.** On systems without an OS keyring, the
> `--allow-plaintext` fallback writes credentials (including the Google
> `client_secret`) to a 0600-mode file under `~/.config/chroncal/`. The mode
> protects against casual `cat`, but not against backups, filesystem
> snapshots, or sync tools (Dropbox, iCloud, rsync) that ignore Unix
> permissions. Install a keyring provider (e.g. `libsecret` + `gnome-keyring`
> on Linux) before using OAuth on shared or backed-up hosts.

1. Create a **Desktop app** OAuth client in the [Google Cloud Console](https://console.cloud.google.com/apis/credentials). Note both the client ID and the client secret.
2. Add `https://www.googleapis.com/auth/calendar` to the OAuth consent screen, and add yourself as a Test user while it's in Testing mode.
3. Enable **both** APIs on the project — they are separate services:

   ```bash
   gcloud services enable calendar-json.googleapis.com --project=YOUR_PROJECT
   gcloud services enable caldav.googleapis.com         --project=YOUR_PROJECT
   ```

   The Calendar JSON API alone is not enough; the CalDAV endpoint will return
   `403 accessNotConfigured` until `caldav.googleapis.com` is enabled.

4. Create the calendar with the Google CalDAV URL attached. Provide the
   client secret via the `GOOGLE_CLIENT_SECRET` environment variable, or let
   chroncal prompt for it interactively (echo disabled). The secret is
   intentionally **not** accepted as a CLI flag — flags leak via process
   listings and shell history.

   ```bash
   GOOGLE_CLIENT_SECRET="GOCSPX-…" chroncal calendar create "Work" \
     --remote-url "https://apidata.googleusercontent.com/caldav/v2/YOUR_CALENDAR_ID/events/" \
     --username "you@example.com" \
     --auth oauth2 \
     --oauth-client-id "YOUR_CLIENT_ID.apps.googleusercontent.com"
   ```

5. Run sync and inspect status:

   ```bash
   chroncal sync run --calendar "Work"
   chroncal sync status
   ```

You can also connect a Google calendar and re-authenticate an expired one
directly from the TUI: open a calendar (the sidebar shows a ⚠ when its last
sync failed) and use the **Re-authenticate** action, or pick **Google OAuth**
as the auth type when creating one. The browser authorization runs without
leaving the app.

Google limitations:

- Google CalDAV only supports `VEVENT`. Use Nextcloud, Radicale, or Fastmail for `VTODO` and `VJOURNAL`.
- Google paginates large `sync-collection` REPORT responses (RFC 6578 §3.6),
  returning a `507` marker plus a continuation token; chroncal follows the
  pages and applies the union, so the initial sync of a big calendar pulls
  every event.

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
chroncal service install              # Install systemd timer (Linux) or launchd agent (macOS)
chroncal service run                  # Run one background-service cycle now (alias: tick)
chroncal service uninstall
chroncal service status
```

### Global flags

All commands accept `-o, --output {text,json}` (default: text).

### Scripting and LLM use

The CLI is meant to be driven from shells and language models, not just typed by hand. The agent-friendly path:

- Pass `-o json` (or `--output json`) on every read/write command. The shape is stable, omits empty optional fields, and write commands return the new row so a script can capture the `id` / `uid`. This applies to read commands too — `sync status`, `sync conflicts`, `freebusy`, and `alarm list` all emit JSON arrays/objects under `-o json`; an empty result is `[]`, not prose.
- Timestamps in JSON are RFC 3339 UTC with a `Z` suffix (`2026-04-21T13:00:00Z`). Text mode prints in your local timezone; only JSON normalizes to UTC so cross-machine comparisons stay honest.
- Check the exit code. `0` on success, non-zero on any failure. Errors go to **stderr**, never stdout, so `cmd -o json | jq …` is safe — on failure stdout is empty.
- Errors honor `-o json`. They emit one JSON object on stderr with a `code` field:

  ```json
  {"code": "not_found", "error": "event 999 not found"}
  ```

  Codes are `not_found`, `invalid_input`, `aborted`, or `error` (catch-all). The `error` field is the user-facing message; internal call-chain prefixes (e.g. `get event:`) are stripped, so dispatch on `code` and surface `error` directly.
- References accept either the numeric `id` or the string `uid`. Recurring overrides additionally take `--recurrence-id <RFC3339>` to target a single instance.
- Dates are `YYYY-MM-DD`. Times are `HH:MM` local unless a command accepts `--timezone`. Durations are Go-style (`30m`, `1h30m`) and some flags also accept RFC 5545 (`PT1H30M`).
- If you want plain text (no JSON), pass `--compact` for one line per row, suitable for `grep`, `awk`, and friends. Available on `event list`, `event search`, `todo list`, `journal list`, and `calendar list`.

```bash
# Round-trip: create then read back the new event
uid=$(chroncal event add "Demo" --date 2026-06-01 --time 09:00 --output json | jq -r .uid)
chroncal event get "$uid" --output json
```

### Destructive operations

`event delete`, `todo delete`, `journal delete`, and `calendar delete` prompt
for confirmation before destroying data. The prompt is bypassed when any of
the following is true, so scripted use keeps working:

- `--yes` / `-y` is passed
- `CHRONCAL_ASSUME_YES=1` is set in the environment
- `--output` is `json` (machine-readable implies scripted)

In a non-interactive shell without any of the above, the command refuses
rather than silently auto-confirming.

### Soft-delete + restore

Events, todos, and journals are soft-deleted by default. The row stays in
the database with a `deleted_at` timestamp so you can restore it later.
After a retention window (default 30 days) a background purge hard-deletes
rows older than the cutoff.

```
chroncal event   restore <id|uid>
chroncal todo    restore <id|uid>
chroncal journal restore <id|uid>

chroncal event   purge-deleted [--older-than DURATION] [--yes]
chroncal todo    purge-deleted [--older-than DURATION] [--yes]
chroncal journal purge-deleted [--older-than DURATION] [--yes]
```

List soft-deleted candidates with `--include-deleted` on the matching
`list` command. In the TUI, press `D` to open the mixed "Recently deleted"
dialog which spans all three resource types; `r` restores the cursor row,
`x` purges it, and space toggles multi-select so you can bulk restore or
bulk purge.

## TUI

Run `chroncal` with no arguments to launch the interactive terminal interface.

**Views**: month, week, day, agenda. Switch with `m`, `w`, `d`, `a`.

The TUI supports creating, editing, viewing, and deleting events, with full
details including alarms, attendees, and attachments. Use `u` to undo a
delete. Calendars are browsable in a sidebar with create / edit / delete
from the calendar popup. Todo and journal management live in the CLI for
now.

Sync health is visible at a glance: a calendar whose last sync failed shows
a `⚠` next to it in the sidebar, and opening it explains why (and offers a
fix). Remote calendars can be connected and re-authenticated without leaving
the TUI — see [Google Calendar via CalDAV](#google-calendar-via-caldav) for
the OAuth flow.

## Configuration

Configuration is loaded in order of precedence:

1. **Environment variables** (prefix `CHRONCAL_`, e.g., `CHRONCAL_DB`)
2. **Config file** at `$XDG_CONFIG_HOME/chroncal/config.toml` (or `~/.config/chroncal/config.toml`)
3. **Defaults**

### Config keys

| Key | Description | Default |
|-----|-------------|---------|
| `db` | Path to SQLite database | `$XDG_DATA_HOME/chroncal/chroncal.db` |
| `product_id` | iCal PRODID for export | `-//chroncal//chroncal//EN` |
| `ui.theme` | Built-in TUI theme name under `internal/tui/themes/` (`system` or `default`; see [TUI themes](#tui-themes)) | `system` |
| `soft_delete.purge_days` | Days to retain soft-deleted rows before the background purge. `0` disables automatic purging. | `30` |
| `sync.interval` | Minimum interval between background CalDAV syncs performed by `chroncal service run` | (unset — sync runs every tick) |
| `sync.conflict_strategy` | Default conflict-resolution mode when `sync run --conflict` is not passed | (unset) |
| `security.allow_unsafe_alarm_audio_attach` | Allow AUDIO alarms to attach arbitrary URIs. Off by default. | `false` |
| `security.allow_unsafe_alarm_email_attendees` | Allow EMAIL alarms to send to unverified attendee addresses. Off by default. | `false` |

Every key is also available as an environment variable (`CHRONCAL_` prefix,
dots become underscores): for example `CHRONCAL_UI_THEME`,
`CHRONCAL_SOFT_DELETE_PURGE_DAYS`, `CHRONCAL_SYNC_INTERVAL`.

### TUI themes

The TUI ships two built-in themes:

- **`system`** (default) — chrome (text, borders, surfaces, dim text)
  inherits the terminal's ANSI palette (`color0..15`), so the TUI follows
  themed terminal setups like
  [Omarchy](https://learn.omacom.io/2/the-omarchy-manual/52/themes),
  Catppuccin, Gruvbox, Tokyo Night, or anything that paints the standard
  16 colors in your terminal config. The row-selection highlight adapts to
  the live terminal background via OSC 11. Accent colors (buttons,
  badges, "today", errors) sit on a fixed Dracula palette so the
  text-on-accent contrast stays guaranteed across themes.
- **`default`** — fixed designer palette (violet primary, sky secondary,
  emerald accent) with light/dark variants. Ignores the terminal palette.
  Pick this if you don't theme your terminal or want the same look on
  every machine.

Override with `ui.theme = "default"` in `config.toml` or
`CHRONCAL_UI_THEME=default`.

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

For safety, chroncal applies size limits to untrusted imports and inline
attachments before storing them locally or sending them to linked CalDAV
servers.

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
