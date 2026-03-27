.PHONY: build run generate lint clean

build:
	go build -o tcal ./cmd/tcal

run: build
	./tcal

generate:
	sqlc generate

lint:
	go vet ./...

clean:
	rm -f tcal
