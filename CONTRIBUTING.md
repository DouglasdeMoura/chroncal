# Contributing to chroncal

Thanks for your interest in contributing. This guide covers development setup, testing, code generation, and conventions.

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

That's it. The database is SQLite (pure Go driver, no CGO), so there are no system dependencies.

## Project structure

```
chroncal/
├── cmd/chroncal/     # CLI commands (cobra)
├── internal/
│   ├── alarm/        # Alarm checking, firing, state
│   ├── app/          # Application initialization
│   ├── auth/         # CalDAV auth (basic, bearer, OAuth2 PKCE, keyring)
│   ├── caldav/       # CalDAV client (discovery, REPORT, free/busy)
│   ├── calendar/     # Calendar service
│   ├── config/       # Configuration loading
│   ├── duration/     # RFC 5545 duration parsing
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
│   ├── sync/         # CalDAV sync engine, conflict handling
│   ├── testutil/     # Test helpers
│   ├── textsafe/     # Safe rendering of untrusted strings
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
make test         # Run all tests (disables caching)
make test-race    # Run tests with the race detector (matches CI)
make coverage     # Run tests and emit coverage.out + textual summary
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
```

## Git hooks

This repo ships a [lefthook](https://lefthook.dev) config that runs the
fast quality checks on every commit and the race-enabled test suite on
every push.

```bash
# one-time install per clone
go install github.com/evilmartians/lefthook@latest
lefthook install
```

Skip a single run with `LEFTHOOK=0 git commit ...` when you need to.

## Testing

Tests use in-memory SQLite databases via `testutil.NewTestDB(t)`, so no external setup is needed.

```bash
# Run all tests
make test

# Run a specific package
go test ./internal/event -v -count=1

# Run a specific test
go test ./internal/event -run TestEventService_Create -v -count=1
```

Always use `-count=1` to bypass Go's test cache.

### Test conventions

- Table-driven tests for multiple cases
- Test both success and error paths
- Integration tests end with `_integration_test.go`
- Use `context.Background()` for test contexts

## Database changes

### Adding a migration

1. Create `db/migrations/{next_number}_description.sql`
2. Include both `-- +goose Up` and `-- +goose Down` sections
3. Update affected queries in `db/queries/*.sql`
4. Run `make generate`
5. Update service code to handle the new schema

Migrations use [goose](https://github.com/pressly/goose) format and run automatically on startup.

### Adding or modifying queries

1. Edit the relevant file in `db/queries/*.sql` using [sqlc syntax](https://docs.sqlc.dev/)
2. Run `make generate`
3. Never edit `internal/storage/*.sql.go` directly, those files are generated

## Code conventions

### Architecture

Each domain (event, todo, calendar, alarm, recurrence) follows the same pattern:

```go
type Service struct {
    db *sql.DB
    q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
    return &Service{db: db, q: q}
}
```

CLI commands live in `cmd/chroncal/`, one file per resource group. Each exports a `Command()` function returning a `*cobra.Command`.

### Naming

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

When touching import/export code, follow RFC 5545. Round-trip fidelity matters: importing and re-exporting a `.ics` file should preserve all properties.

## Releases (maintainers)

Releases are fully automated: bump the `VERSION` file, push a matching `v*`
tag, and GoReleaser publishes the GitHub Release, Homebrew cask, Scoop
manifest, and both AUR packages. The step-by-step checklist, required
secrets, and failure-recovery procedure live in the
[Maintainer checklist](README.md#maintainer-checklist) section of the README.

## Need help?

Open an issue on [GitHub](https://github.com/DouglasdeMoura/chroncal/issues).
