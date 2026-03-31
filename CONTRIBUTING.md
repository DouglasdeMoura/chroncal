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
├── cmd/chroncal/           # CLI commands (cobra)
├── internal/
│   ├── alarm/          # Alarm checking, firing, state
│   ├── app/            # Application initialization
│   ├── calendar/       # Calendar service
│   ├── config/         # Configuration loading
│   ├── duration/       # RFC 5545 duration parsing
│   ├── event/          # Event service and models
│   ├── ical/           # iCal import/export
│   ├── model/          # Shared models (Alarm, Attendee, etc.)
│   ├── notify/         # Desktop notifications
│   ├── recurrence/     # RRULE expansion
│   ├── storage/        # Database layer (sqlc-generated)
│   ├── testutil/       # Test helpers
│   ├── todo/           # Todo service and models
│   └── tui/            # Terminal UI (bubbletea)
├── db/
│   ├── migrations/     # SQL schema migrations (goose)
│   └── queries/        # SQL queries for sqlc
├── sqlc.yaml           # sqlc configuration
├── Makefile            # Build commands
└── go.mod
```

## Common commands

```bash
make build      # Build the chroncal binary
make test       # Run all tests (disables caching)
make generate   # Regenerate Go code from SQL queries
make lint       # Run go vet
make clean      # Remove binary
```

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

## Need help?

Open an issue on [GitHub](https://github.com/DouglasdeMoura/chroncal/issues).
