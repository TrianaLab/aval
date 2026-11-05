.PHONY: build test lint fmt

build:
	go build -o bin/aval ./cmd/aval

test:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run ./...

fmt:
	gofmt -w .
