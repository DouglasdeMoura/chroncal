.PHONY: build run test generate lint clean

build:
	go build -o tcal ./cmd/tcal

run: build
	./tcal

test:
	go test ./internal/... -count=1

generate:
	sqlc generate

lint:
	go vet ./...

clean:
	rm -f tcal
