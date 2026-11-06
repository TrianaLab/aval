.PHONY: build test test-unit coverage coverage-check test-integration test-architecture test-acceptance test-release \
	lint fmt fmt-check example fuzz vuln goreleaser release-snapshot ci ci-static ci-gates ci-engine ci-false-hit ci-e2e ci-release ci-fuzz ci-security \
	docs-lint docs-cli docs-wasm docs-site docs-demo-smoke docs-deploy-test docs-nav-smoke docs-demos ci-docs brand

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

# Level 5 — release. A snapshot dry-run of the release build (.goreleaser.yaml:
# binaries, archives, checksums, SBOMs; unsigned, nothing published), then the
# tests/release assertions against its dist/. The release workflow runs the
# same tests against the tagged, signed build before publishing it.
# GoReleaser is installed into bin/ like govulncheck; SBOMs need syft on PATH
# (ci.yaml installs SYFT_VERSION).
GORELEASER_VERSION := v2.20.0

goreleaser:
	CGO_ENABLED=0 GOBIN=$(CURDIR)/bin go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

release-snapshot: goreleaser
	bin/goreleaser release --snapshot --clean --skip=sign

test-release: release-snapshot
	AVAL_RELEASE_DIST=$(CURDIR)/dist go test -count=1 ./tests/release/...

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
	cd examples/evidence-consumer && ../../bin/govulncheck ./...

# One golangci-lint version for local runs and CI (ci.yaml installs the same
# one; tests/architecture enforces it).
GOLANGCI_LINT_VERSION := v2.15.0

lint:
	go vet ./...
	@v=$$(golangci-lint version --short 2>/dev/null); if [ "v$$v" != "$(GOLANGCI_LINT_VERSION)" ]; then \
		echo "golangci-lint $(GOLANGCI_LINT_VERSION) required, found $${v:-none}"; exit 1; fi
	golangci-lint run ./...
	GOOS=js GOARCH=wasm go vet ./cmd/aval-wasm
	GOOS=js GOARCH=wasm golangci-lint run ./cmd/aval-wasm
	cd examples/evidence-consumer && golangci-lint run ./...
	shellcheck scripts/*.sh docs/assets/tapes/*.sh

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# Docs gates. DOCS_MD is the one list both linters read: every Markdown file
# except the agent skills (.agents/, .claude/), which are maintained apart,
# CLAUDE.md, which is only an include of AGENTS.md, and the Changesets entries
# (.changeset/) and the CHANGELOG.md generated from them.
# Links are checked by tests/architecture/docs_test.go (ci-gates); external
# URLs are not fetched, so the gates stay deterministic and offline.
DOCS_MD = $(shell find . -name '*.md' -not -path './.git/*' -not -path './.agents/*' \
	-not -path './.claude/*' -not -path './CLAUDE.md' -not -path './.changeset/*' -not -path './CHANGELOG.md' \
	-not -path '*/node_modules/*' | sort)
MARKDOWNLINT_CLI2_VERSION := 0.24.0
# Vale ships as a binary (building it needs cgo); install the release whose
# sha256 ci.yaml pins, and `make docs-lint` refuses any other version.
VALE_VERSION := 3.21.0

docs-lint:
	npx --yes markdownlint-cli2@$(MARKDOWNLINT_CLI2_VERSION) $(DOCS_MD)
	@v=$$(vale --version 2>/dev/null | awk '{print $$3}'); if [ "$$v" != "$(VALE_VERSION)" ]; then \
		echo "vale $(VALE_VERSION) required, found $${v:-none}"; exit 1; fi
	vale $(DOCS_MD)

# The MkDocs site (mkdocs.yml) into site/. --strict turns every warning into a
# failure: a page missing from the nav, a broken link or anchor, a missing
# snippet. Install the pinned tools with
# `pip install --require-hashes -r docs/requirements.txt` (regenerate that file
# from docs/requirements.in with `uv pip compile --universal --python-version
# 3.12 --generate-hashes`); preview with `mkdocs serve`. NO_MKDOCS_2_WARNING
# silences a banner Material prints on every build.
docs-site: docs-wasm
	NO_MKDOCS_2_WARNING=1 mkdocs build --strict

# The home page's "Try it" terminal: internal/demo compiled to WebAssembly
# (cmd/aval-wasm) into docs/assets/demo/aval.wasm (not committed), so every
# site build, mike's included, ships it. wasm_exec.js is vendored beside it
# and must be the one of the Go toolchain building it; to update it after a
# Go upgrade: cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" docs/assets/demo/.
# The gzipped wasm must stay within WASM_BUDGET bytes (3 MB).
WASM_BUDGET := 3145728

docs-wasm:
	@cmp -s "$$(go env GOROOT)/lib/wasm/wasm_exec.js" docs/assets/demo/wasm_exec.js || { \
		echo "docs/assets/demo/wasm_exec.js is not the one of $$(go env GOVERSION): vendor it again (see the Makefile)"; exit 1; }
	GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o docs/assets/demo/aval.wasm ./cmd/aval-wasm
	@size=$$(gzip -9 -c docs/assets/demo/aval.wasm | wc -c | tr -d ' '); \
	echo "aval.wasm: $$size bytes gzipped, budget $(WASM_BUDGET)"; \
	if [ "$$size" -gt $(WASM_BUDGET) ]; then echo "aval.wasm is over its size budget"; exit 1; fi

# Headless Chrome against the built site/: clicks "Try it", types aval run
# twice (verified), then tamper and aval verify (exit 5). Needs Node and
# Chrome or Chromium (CHROME=<path> to choose one).
docs-demo-smoke: docs-site
	node scripts/demo-smoke.mjs site

# scripts/deploy-docs.sh (the docs.yaml deploy) against a scratch copy with a
# local origin: default version, `latest` alias, the root installers and
# stats.json (scripts/publish-stats.sh).
docs-deploy-test:
	scripts/test-deploy-docs.sh

# The built site (site/, from `make docs-site`) at a phone width, 390x844:
# tapping the hamburger menu on the home page and on an inner page must slide
# in the primary nav, on screen, above the overlay and with links, in both
# schemes. Headless Chrome over the DevTools protocol; CHROME overrides the
# binary. Pass SHOTS=<dir> to keep the screenshots.
docs-nav-smoke:
	node scripts/docs-nav-smoke.mjs site $(SHOTS)

# Regenerate docs/cli-reference.md from the cobra command tree.
docs-cli:
	go test -count=1 -run '^TestCLIReference$$' ./cmd/aval -update

# Re-record the docs/quickstart.md GIFs from docs/assets/tapes/*.tape with
# this checkout's bin/aval: real runs against a throwaway registry:2 on
# localhost:5001 (the port the quickstart uses). Needs vhs, Docker and
# openssl. The tapes share one project and run in order: miss, hit, changed
# input, tampered. Commit the GIFs; tests/architecture keeps each under 1.5 MB.
DEMO_REGISTRY = aval-docs-demos-registry
DEMO_TAPES = miss hit changed-input tampered

docs-demos: build
	@dir=$$(mktemp -d); trap 'docker rm -f $(DEMO_REGISTRY) >/dev/null 2>&1; rm -rf "$$dir"' EXIT; \
	docker run -d --name $(DEMO_REGISTRY) -p 5001:5000 registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373 >/dev/null && \
	sh docs/assets/tapes/setup.sh "$$dir" && \
	for t in $(DEMO_TAPES); do \
		AVAL_DEMO_DIR="$$dir" AVAL_DEMO_BIN=$(CURDIR)/bin vhs docs/assets/tapes/$$t.tape || exit 1; \
	done

# Render the committed brand PNGs from their SVG sources.
# Run it after changing a logo SVG and commit the result.
RESVG = npx --yes @resvg/resvg-js-cli@2.6.2-beta.1
BRAND = docs/assets/images

brand:
	$(RESVG) $(BRAND)/social-preview.svg $(BRAND)/social-preview.png
	$(RESVG) --fit-width 16 docs/favicon.svg $(BRAND)/favicon-16.png
	$(RESVG) --fit-width 32 docs/favicon.svg $(BRAND)/favicon-32.png

# The example modules: go-compose, and evidence-consumer, a third party that
# builds against this checkout's pkg/evidence only (#18).
example:
	cd examples/go-compose && go vet ./... && go test ./...
	cd examples/evidence-consumer && go vet ./... && go test ./...

# `make ci` is the gate CI runs and the one agents and humans run before
# pushing. Each ci-* leg is one job in .github/workflows/ci.yaml, aggregated by
# the `required` job (tests/architecture/ci_test.go enforces both).
ci: ci-static ci-gates ci-engine ci-false-hit ci-e2e ci-release ci-fuzz ci-security ci-docs

ci-static: fmt-check lint
ci-gates: test-architecture
ci-engine: coverage-check example
# The false-hit suite (#15) as its own gate, never cached: the corpus of
# relevant and irrelevant changes (TestFalseHitCorpus), the manifest goldens
# that fail on a change to the golden tree's manifests without a
# fingerprint.Version bump (TestManifestGolden), the append-only check on
# committed goldens (TestGoldensAppendOnly, against AVAL_GOLDEN_BASE, which
# defaults to origin/main here; it fails if the base does not resolve and
# skips only outside this leg), the glob tables and the fuzz seeds. ci-engine
# also runs these packages; this leg is uncached and runs on docs-only PRs
# too. The recipe is pinned by TestFalseHitGateIsBinding. Unix only: the
# golden sets exec bits.
ci-false-hit:
	AVAL_GOLDEN_BASE="$${AVAL_GOLDEN_BASE-origin/main}" go test -count=1 ./internal/fingerprint/... ./internal/contract/...
# Levels 2 and 4, the heavy suite, through `aval run` with the contract
# aval.e2e.yaml. Locally (no AVAL_REGISTRY, AVAL_POLICY) it always runs. CI
# sets both, so a verified passing result for the same inputs is reused and
# anything unproven runs (invariant 3); a verification failure runs the suite
# and exits 5. The report is in the `aval verify --output json` format. The
# run record (statistics) goes to .aval/e2e-record.json; on main CI publishes
# it with `aval record` from a job that runs no tests.
ci-e2e: build
	bin/aval run -f aval.e2e.yaml --result .aval/e2e-result.json --report .aval/e2e-report.json \
		--record-file .aval/e2e-record.json $${AVAL_CI_RUN_URL:+--ci-run-url "$$AVAL_CI_RUN_URL"} $${AVAL_REPOSITORY:+--repository "$$AVAL_REPOSITORY"} \
		$${AVAL_REGISTRY:+--registry "$$AVAL_REGISTRY"} $${AVAL_POLICY:+--policy "$$AVAL_POLICY"}
ci-release: test-release
ci-fuzz: fuzz
ci-security: vuln
# Fails when docs/cli-reference.md drifts from the CLI (TestCLIReference) or
# the site does not build strictly, the "Try it" terminal does not work in a
# browser, the mobile drawer does not open, or the deploy logic breaks.
ci-docs: docs-lint docs-site docs-demo-smoke docs-nav-smoke docs-deploy-test
	go test -count=1 -run '^TestCLIReference$$' ./cmd/aval
