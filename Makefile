.PHONY: build test test-unit coverage coverage-check test-integration test-architecture test-acceptance test-release \
	lint fmt fmt-check example fuzz vuln ci ci-static ci-gates ci-engine ci-integration ci-acceptance ci-release ci-fuzz ci-security \
	docs-lint docs-cli ci-docs

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

# Go native fuzzing: every Fuzz* target in the unit packages, one at a time
# (go test -fuzz takes one target per run), FUZZTIME each. A crasher is
# written to the package's testdata/fuzz/<Target>/ and from then on runs as a
# regression case in plain `go test`; commit it with the fix.
FUZZTIME ?= 10s

fuzz:
	@set -e; for pkg in $(UNIT_PKGS); do \
		for t in $$(go test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			echo "fuzz $$pkg $$t ($(FUZZTIME))"; \
			go test -run '^$$' -fuzz "^$$t$$" -fuzztime $(FUZZTIME) $$pkg; \
		done; \
	done

# govulncheck reports known vulnerabilities that the code can reach, in
# dependencies and in the standard library. It is installed into bin/ and run
# with the project's own go (go.mod), so it checks the standard library Aval
# builds with; `go run ...@version` would scan the newer toolchain govulncheck
# itself requires. Building govulncheck needs go >= 1.26; GOTOOLCHAIN=auto
# (the default) fetches it.
GOVULNCHECK_VERSION := v1.9.0

vuln:
	GOBIN=$(CURDIR)/bin go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	bin/govulncheck ./...
	cd examples/go-compose && ../../bin/govulncheck ./...

# One golangci-lint version for local runs and CI (ci.yaml installs the same
# one; tests/architecture enforces it).
GOLANGCI_LINT_VERSION := v2.13.0

lint:
	go vet ./...
	@v=$$(golangci-lint version --short 2>/dev/null); if [ "v$$v" != "$(GOLANGCI_LINT_VERSION)" ]; then \
		echo "golangci-lint $(GOLANGCI_LINT_VERSION) required, found $${v:-none}"; exit 1; fi
	golangci-lint run ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# Docs gates. DOCS_MD is the one list both linters read: every Markdown file
# except the agent skills (.agents/, .claude/), which are maintained apart, and
# CLAUDE.md, which is only an include of AGENTS.md.
# Links are checked by tests/architecture/docs_test.go (ci-gates); external
# URLs are not fetched, so the gates stay deterministic and offline.
DOCS_MD = $(shell find . -name '*.md' -not -path './.git/*' -not -path './.agents/*' \
	-not -path './.claude/*' -not -path './CLAUDE.md' -not -path '*/node_modules/*' | sort)
MARKDOWNLINT_CLI2_VERSION := 0.24.0
# Vale ships as a binary (building it needs cgo); install the release whose
# sha256 ci.yaml pins, and `make docs-lint` refuses any other version.
VALE_VERSION := 3.21.0

docs-lint:
	npx --yes markdownlint-cli2@$(MARKDOWNLINT_CLI2_VERSION) $(DOCS_MD)
	@v=$$(vale --version 2>/dev/null | awk '{print $$3}'); if [ "$$v" != "$(VALE_VERSION)" ]; then \
		echo "vale $(VALE_VERSION) required, found $${v:-none}"; exit 1; fi
	vale $(DOCS_MD)

# Regenerate docs/cli-reference.md from the cobra command tree.
docs-cli:
	go test -count=1 -run '^TestCLIReference$$' ./cmd/aval -update

example:
	cd examples/go-compose && go vet ./... && go test ./...

# `make ci` is the gate CI runs and the one agents and humans run before
# pushing. Each ci-* leg is one job in .github/workflows/ci.yaml, aggregated by
# the `required` job (tests/architecture/ci_test.go enforces both).
ci: ci-static ci-gates ci-engine ci-integration ci-acceptance ci-release ci-fuzz ci-security ci-docs

ci-static: fmt-check lint
ci-gates: test-architecture
ci-engine: coverage-check example
ci-integration: test-integration
ci-acceptance: test-acceptance
ci-release: test-release
ci-fuzz: fuzz
ci-security: vuln
# Fails when docs/cli-reference.md drifts from the CLI (TestCLIReference).
ci-docs: docs-lint
	go test -count=1 -run '^TestCLIReference$$' ./cmd/aval
