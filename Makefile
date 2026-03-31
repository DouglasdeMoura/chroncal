.PHONY: build run test generate lint clean

build:
	go build -o chroncal ./cmd/chroncal

run: build
	./chroncal

test:
	go test ./internal/... -count=1

generate:
	sqlc generate

lint:
	go vet ./...

clean:
	rm -f chroncal
