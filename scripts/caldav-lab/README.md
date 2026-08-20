# CalDAV account lab

This lab runs the `chroncal account` commands against a real CalDAV server.
The unit tests use fake transports. The Radicale round-trip tests in
`internal/ical` cover the iCal layer. Neither one covers the account
lifecycle end to end. This lab covers that gap.

## What the lab needs

- Docker with the `compose` plugin
- `curl`
- A Go toolchain, for `make build`

## How to run the lab

```bash
scripts/caldav-lab/run-account-lab.sh
```

The script starts the server, seeds the collections, builds the binary, and
runs the full lifecycle. Set `KEEP_DB=1` to keep the temporary database for
inspection.

To stop the server and to delete its data:

```bash
docker compose -f scripts/caldav-lab/docker-compose.yml down -v
```

## What the lab covers

The seed script creates five collections:

| Collection           | Component  | Purpose                                  |
| -------------------- | ---------- | ---------------------------------------- |
| `Personal`           | `VEVENT`   | A usable event collection                |
| `Holidays in Brazil` | `VEVENT`   | A second event collection, with a space  |
| `Família`            | `VEVENT`   | A non-ASCII display name                 |
| `Tasks`              | `VTODO`    | A todo collection                        |
| `Availability`       | `VFREEBUSY`| An unusable collection, to skip          |

The lab script then runs this sequence:

1. `account add` discovers the collections and imports the four usable ones.
   It skips the `VFREEBUSY` collection as unsupported.
2. `account calendars list` refreshes the complete inventory.
3. The initial sync pulls the three seeded events. The script asserts this.
4. `event add` plus `sync run` pushes a local event to the server.
5. `account calendars set` reduces the local selection to two calendars. The
   script asserts which remote paths stay and which ones go.
6. `account remove` deletes the credential and keeps the local copies.

The remote `Personal` collection arrives with the local name `Personal (2)`,
because the local default calendar already holds the name `Personal`. The
assertions match on the remote path for that reason.

The script exits with a non-zero status when an assertion fails.

## The port

The lab server listens on port 5233. The anonymous server for the
`internal/ical` round-trip tests listens on port 5232. The two servers can
run at the same time.

## Warning

Do not expose this server to a network. The credential is `alice:secret`
and the transport is HTTP.
