# Radicale — local CalDAV test server

A throwaway [Radicale](https://radicale.org/) server for exercising
chroncal's CalDAV sync and iCal round-trip against a real server. It
supports **VEVENT**, **VTODO**, and **VJOURNAL**, which makes it the
target for the integration tests in
[`internal/ical/radicale_test.go`](../../internal/ical/radicale_test.go).

## Usage

```bash
# Start (detached). Requires Docker + the compose plugin.
docker compose -f tools/radicale/docker-compose.yml up -d

# It serves at http://localhost:5232 with NO authentication.

# Stop, keeping stored collections in the named volume:
docker compose -f tools/radicale/docker-compose.yml down

# Stop and wipe all stored collections:
docker compose -f tools/radicale/docker-compose.yml down -v
```

## Configuration

| File                 | Purpose                                                        |
| -------------------- | -------------------------------------------------------------- |
| `docker-compose.yml` | Runs `tomsquest/docker-radicale` on port 5232, persistent vol. |
| `config`             | `auth = none`, filesystem storage, `rights = from_file`.       |
| `rights`             | Grants every (anonymous) user full read/write to all paths.    |

This matches what the integration tests expect: anonymous `MKCOL` /
`MKCALENDAR` / `PUT` on `localhost:5232`, no credentials.

> ⚠️ **Local testing only.** Authentication is disabled and every
> collection is world-writable. Never expose this to a network.

## Using it from chroncal

Point a calendar's remote link at a collection on this server (create one
first with `MKCALENDAR`, or let a sync push create it), e.g.
`http://localhost:5232/qauser/qa/`. Because auth is disabled, any
username works and no password is required.

## Running the integration tests

```bash
RADICALE_URL=http://localhost:5232 go test ./internal/ical -run Radicale -v -count=1
```

`RADICALE_URL` defaults to `http://localhost:5232`; set it to target a
server running elsewhere.
