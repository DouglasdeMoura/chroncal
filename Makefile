.PHONY: build run test coverage generate lint clean

build:
	go build -o chroncal ./cmd/chroncal

run: build
	./chroncal

test:
	go test ./internal/... -count=1

coverage:
	go test ./internal/... -count=1 -coverprofile=coverage.out
	go tool cover -func=coverage.out

generate:
	sqlc generate

lint:
	golangci-lint run ./...

clean:
	rm -f chroncal coverage.out
