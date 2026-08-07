.PHONY: build run test test-race coverage generate \
        fmt fmt-check vet lint staticcheck vulncheck tidy-check \
        check tools clean clean-db

build:
	go build -o chroncal ./cmd/chroncal

run: build
	./chroncal

test:
	go test ./... -count=1

test-race:
	go test -race -count=1 ./...

coverage:
	go test ./... -count=1 -coverprofile=coverage.out
	go tool cover -func=coverage.out

generate:
	sqlc generate

# --- Code quality --------------------------------------------------------

# Scope gofmt to this module's own packages. A bare `find .` also sweeps
# gitignored third-party checkouts that carry their own go.mod (.crush/crush is
# ~480 files): `make fmt` would rewrite code this repo does not own, and
# `make check` would fail on upstream formatting nobody here can fix. `go list
# ./...` already skips those, along with dot-prefixed dirs like .worktrees.
GO_PKG_DIRS = go list -f '{{.Dir}}' ./...

fmt:
	@gofmt -w $$($(GO_PKG_DIRS))

fmt-check:
	@diff=$$(gofmt -l $$($(GO_PKG_DIRS))); \
	if [ -n "$$diff" ]; then \
		echo "gofmt diffs in:"; echo "$$diff"; \
		echo "run 'make fmt' to fix"; exit 1; \
	fi

vet:
	go vet ./...

lint:
	golangci-lint run ./...

staticcheck:
	staticcheck ./...

vulncheck:
	govulncheck ./...

tidy-check:
	@cp go.mod go.mod.bak && cp go.sum go.sum.bak; \
	go mod tidy; \
	diff=$$(diff go.mod go.mod.bak || true); sumdiff=$$(diff go.sum go.sum.bak || true); \
	mv go.mod.bak go.mod && mv go.sum.bak go.sum; \
	if [ -n "$$diff" ] || [ -n "$$sumdiff" ]; then \
		echo "go.mod/go.sum not tidy — run 'go mod tidy'"; exit 1; \
	fi

check: fmt-check vet lint vulncheck test-race

# Build the lint/vuln tools with the same toolchain the module targets.
# `go install pkg@latest` runs module-less and ignores this repo's
# `toolchain` directive, so a tool built under an older Go can't parse the
# newer stdlib/config and `make check` fails (lint: "language version used
# to build golangci-lint is lower than the targeted Go version"). GOVERSION
# is queried inside the module, where the toolchain directive applies.
GOTOOLCHAIN_VER := $(shell go env GOVERSION)

tools:
	GOTOOLCHAIN=$(GOTOOLCHAIN_VER) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	GOTOOLCHAIN=$(GOTOOLCHAIN_VER) go install golang.org/x/vuln/cmd/govulncheck@latest
	GOTOOLCHAIN=$(GOTOOLCHAIN_VER) go install honnef.co/go/tools/cmd/staticcheck@latest

# --- Housekeeping --------------------------------------------------------

# `clean` removes build outputs only. The repo-local chroncal.db is real user
# data for anyone dogfooding with CHRONCAL_DB=./chroncal.db, and there is no
# trash or backup to recover it from — deleting it as a side effect of a
# routine build clean is not a trade a Makefile gets to make. `clean-db` exists
# for when that is actually what you want.
clean:
	rm -f chroncal coverage.out

clean-db:
	rm -f chroncal.db chroncal.db-wal chroncal.db-shm
