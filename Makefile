.PHONY: build test lint fmt fmt-check example ci ci-static ci-gates ci-engine

build:
	go build -o bin/aval ./cmd/aval

# Level 1 — unit.
test:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

example:
	cd examples/go-compose && go vet ./... && go test ./...

# `make ci` is the gate CI runs and the one agents and humans run before
# pushing. Legs grow with Epic 0 (TrianaLab/aval issue tracker): coverage,
# integration, acceptance, security, docs lint.
ci: ci-static ci-gates ci-engine

ci-static: fmt-check lint

# Level 3 — architecture/invariant gates about the repository itself.
ci-gates:
	go test -count=1 ./tests/architecture/...

ci-engine: test example
