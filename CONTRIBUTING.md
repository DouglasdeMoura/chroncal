# Contribute to chroncal

Thanks for your interest in this project. This guide covers development setup, tests, code generation, and conventions.

## Prerequisites

- [Go](https://go.dev/) 1.25+
- [sqlc](https://sqlc.dev/) (for code generation from SQL queries)

## Setup

```bash
git clone https://github.com/DouglasdeMoura/chroncal.git
cd chroncal
make build
make test
```

The database is SQLite (pure Go driver, no CGO). There are no system dependencies.

## Project structure

```
chroncal/
├── cmd/chroncal/     # CLI commands (cobra)
├── internal/
│   ├── alarm/        # Alarm check, fire, and state
│   ├── app/          # Application setup
│   ├── auth/         # CalDAV auth (basic, bearer, OAuth2 PKCE, keyring)
│   ├── caldav/       # CalDAV client (discovery, REPORT, free/busy)
│   ├── calendar/     # Calendar service
│   ├── config/       # Configuration load
│   ├── duration/     # RFC 5545 duration parser
│   ├── event/        # Event service and models
│   ├── freebusy/     # Local free/busy computation + remote query
│   ├── ical/         # iCal import/export
│   ├── journal/      # Journal service and models
│   ├── maintenance/  # Background soft-delete purge loop
│   ├── model/        # Shared models (Alarm, Attendee, etc.)
│   ├── notify/       # Desktop notifications + SMTP email
│   ├── recurrence/   # RRULE expansion
│   ├── retry/        # HTTP retry/backoff helpers
│   ├── storage/      # Database layer (sqlc-generated + hand-written)
│   ├── sync/         # CalDAV sync engine, conflict resolution
│   ├── testutil/     # Test helpers
│   ├── textsafe/     # Safe display of untrusted strings
│   ├── timeutil/     # Time helpers (ranges, timezones)
│   ├── todo/         # Todo service and models
│   ├── trash/        # Mixed soft-delete / restore across domains
│   └── tui/          # Terminal UI (bubbletea)
├── db/
│   ├── migrations/   # SQL schema migrations (goose)
│   └── queries/      # SQL queries for sqlc
├── sqlc.yaml         # sqlc configuration
├── Makefile          # Build commands
└── go.mod
```

## Common commands

```bash
make build        # Build the chroncal binary
make run          # Build and run chroncal
make test         # Run all tests (no cache)
make test-race    # Run tests with the race detector (matches CI)
make coverage     # Run tests and emit coverage.out + a text summary
make generate     # Regenerate Go code from SQL queries
make fmt          # gofmt -w .
make fmt-check    # Fail if any file needs gofmt
make vet          # go vet ./...
make lint         # golangci-lint run ./...
make staticcheck  # staticcheck ./...
make vulncheck    # govulncheck ./...
make tidy-check   # Fail if go.mod/go.sum would change under `go mod tidy`
make check        # fmt-check + vet + lint + vulncheck + test-race
make tools        # Install govulncheck and staticcheck
make clean        # Remove the binary and coverage output
make clean-db     # Delete the repo-local chroncal.db and its WAL/SHM files
```

## Git hooks

This repo includes a [lefthook](https://lefthook.dev) config. The config runs the fast quality checks on every commit. It also runs the race-enabled test suite on every push.

```bash
# one-time install per clone
go install github.com/evilmartians/lefthook@latest
lefthook install
```

Skip a single run with `LEFTHOOK=0 git commit ...` when you need to.

## Tests

Tests use in-memory SQLite databases via `testutil.NewTestDB(t)`. You do not need extra setup.

```bash
# Run all tests
make test

# Run a specific package
go test ./internal/event -v -count=1

# Run a specific test
go test ./internal/event -run TestEventService_Create -v -count=1
```

Always use `-count=1` to skip the Go test cache.

### Test conventions

- Table-driven tests for multiple cases
- Test both success and error paths
- Integration tests end with `_integration_test.go`
- Use `context.Background()` for test contexts

## Database changes

### Add a migration

1. Create `db/migrations/{next_number}_description.sql`.
2. Include both `-- +goose Up` and `-- +goose Down` sections.
3. Update the related queries in `db/queries/*.sql`.
4. Run `make generate`.
5. Update the service code so it supports the new schema.

Migrations use [goose](https://github.com/pressly/goose) format. The program runs them on startup.

### Add or change queries

1. Edit the related file in `db/queries/*.sql` with [sqlc syntax](https://docs.sqlc.dev/).
2. Run `make generate`.
3. Do not edit `internal/storage/*.sql.go`. Those files are generated.

## Code conventions

### Architecture

Each domain (event, todo, calendar, alarm, recurrence) uses the same pattern:

```go
type Service struct {
    db *sql.DB
    q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
    return &Service{db: db, q: q}
}
```

CLI commands live in `cmd/chroncal/`. There is one file per resource group. Each file exports a `Command()` function. The function returns a `*cobra.Command`.

### Names

- **Go**: `PascalCase` exports, `camelCase` internals
- **SQL tables/columns**: `snake_case`
- **Indexes**: `idx_{table}_{column}`

### Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(scope): add new capability
fix(scope): correct a bug
refactor(scope): restructure without behavior change
test(scope): add or update tests
chore: maintenance tasks
```

### iCal compliance

When you change import or export code, follow RFC 5545. Round-trip fidelity matters. Import a `.ics` file and export it again. The export must keep all properties.

## Releases (maintainers)

Releases run with no manual package steps. Bump the `VERSION` file. Push a `v*` tag that matches the VERSION file. GoReleaser then publishes the GitHub Release, the Homebrew cask, the Scoop manifest, and both AUR packages.

The checklist, the required secrets, and the recovery steps live in the [Maintainer checklist](README.md#maintainer-checklist) section of the README.

## Need help?

Open an issue on [GitHub](https://github.com/DouglasdeMoura/chroncal/issues).