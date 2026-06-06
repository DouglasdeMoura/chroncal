.PHONY: build run test test-race coverage generate \
        fmt fmt-check vet lint staticcheck vulncheck tidy-check \
        check tools clean

build:
	go build -o chroncal ./cmd/chroncal

run: build
	./chroncal

test:
	go test ./internal/... -count=1

test-race:
	go test -race -count=1 ./...

coverage:
	go test ./internal/... -count=1 -coverprofile=coverage.out
	go tool cover -func=coverage.out

generate:
	sqlc generate

# --- Code quality --------------------------------------------------------

fmt:
	gofmt -w .

fmt-check:
	@diff=$$(gofmt -l .); \
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

clean:
	rm -f chroncal coverage.out
