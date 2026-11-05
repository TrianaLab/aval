.PHONY: build test test-unit coverage coverage-check test-integration test-architecture test-acceptance test-release \
	lint fmt fmt-check example ci ci-static ci-gates ci-engine ci-integration ci-acceptance ci-release

# Level 1 runs every package outside tests/; levels 2-5 live in tests/<level>.
UNIT_PKGS = $(shell go list ./... | grep -v /tests/)

build:
	go build -o bin/aval ./cmd/aval

# Level 1 — unit.
test: test-unit
test-unit:
	go test -race -coverprofile=coverage.out $(UNIT_PKGS)

# Coverage gate: 100.0% of statements outside tests/ and examples/. The only
# exclusion is the one-line main() in cmd/aval (it calls os.Exit); its block
# is dropped by line number, so any logic added to main() fails the gate.
coverage-check: test-unit
	@line=$$(grep -n '^func main()' cmd/aval/main.go | cut -d: -f1); \
	grep -v "/cmd/aval/main.go:$$line\." coverage.out > coverage.gate.out; \
	total=$$(go tool cover -func=coverage.gate.out | awk '/^total:/ {print $$3}'); \
	echo "total coverage: $$total"; \
	if ! awk 'NR>1 {t+=$$2; if ($$3>0) c+=$$2} END {exit !(t==c)}' coverage.gate.out; then \
		go tool cover -func=coverage.gate.out | grep -v '100.0%'; \
		echo "coverage gate: below 100.0%"; exit 1; \
	fi

# HTML report of the unit coverage profile, written to coverage.html.
coverage: test-unit
	go tool cover -html=coverage.out -o coverage.html

# Level 2 — integration: real components wired together, no network.
test-integration:
	go test -race -count=1 ./tests/integration/...

# Level 3 — architecture/invariant gates about the repository itself.
test-architecture:
	go test -count=1 ./tests/architecture/...

# Level 4 — local acceptance: whole user stories through the CLI binary.
test-acceptance:
	go test -count=1 ./tests/acceptance/...

# Level 5 — release.
test-release:
	go test -count=1 ./tests/release/...

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
# pushing. Each ci-* leg is one job in .github/workflows/ci.yaml, aggregated by
# the `required` job (tests/architecture/ci_test.go enforces both).
ci: ci-static ci-gates ci-engine ci-integration ci-acceptance ci-release

ci-static: fmt-check lint
ci-gates: test-architecture
ci-engine: coverage-check example
ci-integration: test-integration
ci-acceptance: test-acceptance
ci-release: test-release
