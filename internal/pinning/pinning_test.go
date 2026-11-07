package pinning

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const dg = "@sha256:c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b"
const sha = "0123456789abcdef0123456789abcdef01234567"

// detect runs every detector that applies to path, with scope as the other
// in-scope files, and returns the refs found.
func detect(path, data string, scope ...string) []string {
	has := func(p string) bool { return p == path || slices.Contains(scope, p) }
	var refs []string
	for _, d := range Detectors {
		if d.Applies(path) {
			for _, f := range d.Detect(Input{Path: path, Data: []byte(data), Has: has}) {
				refs = append(refs, f.Ref)
			}
		}
	}
	return refs
}

func cmdRefs(command string, scope ...string) []string {
	var refs []string
	for _, f := range scanShell(CommandFile, command, func(p string) bool { return slices.Contains(scope, p) }) {
		refs = append(refs, f.Ref)
	}
	return refs
}

type fileCase struct {
	name, path, data string
	scope            []string
	want             []string // refs; nil means pinned
}

// The false-hit table per ecosystem: each mutable reference must be found
// (a miss means a repushed tag or a new release could be reused), and each
// pinned variant must not (a finding there is a needless miss).
var fileCases = []fileCase{
	// Container images.
	{name: "compose tag", path: "compose.yaml", data: "services:\n  db:\n    image: postgres:16\n", want: []string{"postgres:16"}},
	{name: "compose no tag", path: "x/docker-compose.yml", data: "services: {db: {image: x}}\n  - image: 'redis' # cache\n", want: []string{"x", "redis"}},
	{name: "compose digest", path: "compose.yaml", data: "    image: \"postgres:16" + dg + "\"\n", want: nil},
	{name: "compose variable", path: "compose.yaml", data: "    image: ${DB_IMAGE:-postgres:16}\n", want: []string{"${DB_IMAGE:-postgres:16}"}},
	{name: "helm template", path: "k8s/deploy.yaml", data: "      - image: {{ .Values.image }}\n", want: []string{"{{ .Values.image }}"}},
	{name: "k8s list item", path: "k8s/deploy.yaml", data: "      - name: app\n        image: ghcr.io/acme/app:1.2\n", want: []string{"ghcr.io/acme/app:1.2"}},
	{name: "helm values mapping", path: "values.yaml", data: "image:\n  repository: x\n  tag: y\nimage: &v\n  tag: z\n# image: redis\n", want: nil},
	{name: "compose anchor", path: "compose.yaml", data: "    image: &img redis:latest\n    image: &pin \"redis" + dg + "\"\n", want: []string{"redis:latest"}},
	{name: "compose alias and block scalar", path: "compose.yaml", data: "    image: *img\n    image: >-\n      postgres:latest\n    image: |\n    image: !!str redis\n", want: []string{"*img", ">-", "|", "!!str redis"}},
	{name: "compose flow mapping", path: "compose.yaml", data: "  c: {image: \"mysql:8\", restart: always}\n  d: {restart: no, image: mysql" + dg + "}\n", want: []string{"mysql:8"}},
	{name: "compose image on the next line", path: "compose.yaml", data: "services:\n  db:\n    image:\n      postgres:latest\n  web:\n    image: # c\n\n      nginx" + dg + "\n", want: []string{"postgres:latest"}},
	{name: "helm values image mapping", path: "values.yaml", data: "image:\n  repository: x\n  tag: \"1\"\n", want: nil},
	{name: "compose json", path: "compose.json", data: `{"services":{"db":{"image":"postgres:latest"}}}`, want: []string{"postgres:latest"}},
	{name: "other json is not compose", path: "x.json", data: `{"image":"postgres:latest"}`, want: nil},
	{name: "compose quoted key", path: "compose.yaml", data: "    \"image\": \"nginx:latest\"\n    'image': nginx" + dg + "\n", want: []string{"nginx:latest"}},
	// YAML keys by decoded value (#116).
	{name: "yaml escaped key", path: "compose.yaml", data: "services:\n  db:\n    \"ima\\x67e\": evil:latest\n", want: []string{"evil:latest"}},
	{name: "yaml escaped key pinned", path: "compose.yaml", data: "services:\n  db:\n    \"ima\\u0067e\": \"redis" + dg + "\"\n", want: nil},
	{name: "yaml escaped key in a flow mapping", path: "k8s/pod.yml", data: "c: [{image: a" + dg + "}, {\"ima\\x67e\": evil:1, restart: no}]\n", want: []string{"evil:1"}},
	{name: "yaml escaped key with alias value", path: "compose.yaml", data: "x: &i redis:latest\n\"\\x69mage\": *i\n", want: []string{"*i"}},
	{name: "yaml escaped key with block value", path: "compose.yaml", data: "\"\\x69mage\": |\n  redis\n\"\\x69mage\": !t redis\n\"\\x69mage\": >-\n  redis\n", want: []string{"|", "!t redis", ">"}},
	{name: "yaml escaped key with list value", path: "compose.yaml", data: "\"\\x69mage\": [a]\n", want: []string{"image"}},
	{name: "yaml escaped key with mapping value", path: "values.yaml", data: "\"\\x69mage\":\n  tag: x\n", want: nil},
	{name: "json compose escaped key", path: "compose.json", data: `{"services":{"db":{"\u0069mage":"postgres:latest"},"w":{"image":"nginx` + dg + `"}}}`, want: []string{"postgres:latest"}},
	{name: "yaml explicit, tagged, anchored and nested keys", path: "compose.yaml", data: "? image\n: evil:0\nl:\n- !!str image: evil:1\n- &k image: evil:2\n- - image: evil:3\n- [image: evil:4]\n- *k : evil:5\n", want: []string{"evil:0", "evil:1", "evil:2", "evil:3", "evil:4", "evil:5"}},
	{name: "yaml undecodable escaped key", path: "compose.yaml", data: "\"ima\\qe\": evil:latest\n", want: []string{`"ima\qe"`}},
	{name: "yaml escaped key in invalid yaml", path: "compose.yaml", data: "x: @bad\n\"ima\\x67e\": evil:latest\n\"a\\tb\": evil:latest\n", want: []string{"evil:latest"}},
	{name: "kustomize images", path: "k8s/kustomization.yaml", data: "images:\n- name: nginx\n  newTag: \"1.27\"\n- name: redis\n  newName: ghcr.io/acme/redis\n- name: pg\n  newName: ghcr.io/acme/pg\n  newTag: \"16\"\n  digest: sha256:" + dg[8:] + "\n- name: untouched\n- \"n\\x61me\": a\n  \"newT\\x61g\": b\n", want: []string{"nginx:1.27", "ghcr.io/acme/redis", "a:b"}},
	{name: "kustomize images pinned", path: "Kustomization", data: "resources: [deploy.yaml]\nimages:\n- name: pg\n  digest: sha256:" + dg[8:] + "\n", want: nil},
	{name: "kustomize bad digest", path: "base/kustomization.yml", data: "images:\n- name: pg\n  digest: latest\n", want: []string{"pg@latest"}},
	{name: "kustomize invalid yaml", path: "kustomization.yaml", data: "images: [\n", want: []string{"kustomization.yaml"}},
	{name: "kustomize without images", path: "kustomization.yaml", data: "images: x\n---\n", want: nil},
	{name: "dockerfile tag", path: "Dockerfile", data: "FROM golang:1.25\n", want: []string{"golang:1.25"}},
	{name: "dockerfile platform and stages", path: "build/Containerfile", data: "# c\nFROM --platform=$BUILDPLATFORM golang:1.25" + dg + " AS build\nFROM build AS test\nFROM scratch\nCOPY --from=build /a /b\nCOPY --from=0 /a /b\n", want: nil},
	{name: "dockerfile stage name is not an earlier stage", path: "Dockerfile", data: "FROM build AS build\n", want: []string{"build"}},
	{name: "dockerfile arg default", path: "app.Dockerfile", data: "ARG BASE=alpine:3.20\nFROM ${BASE}\n", want: []string{"alpine:3.20"}},
	{name: "dockerfile arg pinned", path: "Dockerfile.ci", data: "ARG BASE=alpine:3.20" + dg + "\nFROM $BASE\n", want: nil},
	{name: "dockerfile unresolved arg", path: "Dockerfile", data: "ARG BASE\nFROM ${BASE}\n", want: []string{"$BASE"}},
	{name: "dockerfile copy from image", path: "Dockerfile", data: "FROM scratch\nCOPY --from=nginx:latest /a /b\n", want: []string{"nginx:latest"}},
	{name: "dockerfile remote add", path: "Dockerfile", data: "FROM scratch\nADD https://x.io/a.tgz /a\nADD --checksum=sha256:ab https://x.io/b.tgz /b\nADD a b\n", want: []string{"https://x.io/a.tgz"}},
	{name: "dockerfile run", path: "Dockerfile", data: "FROM scratch\nRUN --mount=type=cache,target=/r apk add --no-cache curl \\\n  && pip install requests==2.0\n", want: []string{"curl"}},
	{name: "dockerfile run global options", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN apk --no-cache add curl\nRUN apt-get -y install jq\n", want: []string{"curl", "jq"}},
	{name: "dockerfile syntax directive", path: "Dockerfile", data: "# syntax=docker/dockerfile:1\nFROM alpine" + dg + "\n", want: []string{"docker/dockerfile:1"}},
	{name: "dockerfile syntax directive pinned", path: "Dockerfile", data: "\n# check=skip=all\n#Syntax = docker/dockerfile:1" + dg + "\nFROM alpine" + dg + "\n# syntax=late:1\n", want: nil},
	// BuildKit line rules (#116): a comment line stands alone, continuations
	// join with nothing, comments and blank lines inside one are dropped,
	// and # escape= is honoured only among the leading parser directives.
	{name: "dockerfile comment ending in backslash", path: "Dockerfile", data: "FROM alpine" + dg + "\n# note \\\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile comment ending in backslash crlf", path: "Dockerfile", data: "FROM alpine" + dg + "\r\n  # note \\\r\nFROM evil:latest\r\n", want: []string{"evil:latest"}},
	{name: "dockerfile continuation joins with nothing", path: "Dockerfile", data: "FROM db:1\\\n6\n", want: []string{"db:16"}},
	{name: "dockerfile continuation drops comments and blank lines", path: "Dockerfile", data: "FROM \\\n# c \\\n  \n  db:1\\\n  # d\n6\n", want: []string{"db:16"}},
	{name: "dockerfile digest split across a continuation", path: "Dockerfile", data: "FROM alpine@sha256:" + dg[8:40] + "\\\n# c\n" + dg[40:] + "\n", want: nil},
	{name: "dockerfile escaped backslash is not a continuation", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN echo \\\\\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile escape backtick", path: "Dockerfile", data: "# escape=`\nFROM alpine" + dg + " \\\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile escape backtick continuation", path: "Dockerfile", data: "\ufeff# Escape = ` \nFROM alpine`\n" + dg + "\n", want: nil},
	{name: "dockerfile escape after a comment is not a directive", path: "Dockerfile", data: "# c\n# escape=`\nFROM alpine" + dg + " `\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile escape after a blank line is not a directive", path: "Dockerfile", data: "\n# escape=`\nFROM evil:latest `\nRUN x\n", want: []string{"evil:latest"}},
	{name: "dockerfile escape after an unknown directive is not a directive", path: "Dockerfile", data: "# foo=1\n# escape=`\nFROM alpine" + dg + " `\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile onbuild heredoc", path: "Dockerfile", data: "FROM alpine" + dg + "\nONBUILD COPY <<EOF /x\nFROM evil:latest\nEOF\n", want: nil},
	{name: "dockerfile invalid escape directive", path: "Dockerfile", data: "# escape=x\nFROM alpine" + dg + "\n", want: []string{"# escape=x"}},
	{name: "dockerfile repeated directive", path: "Dockerfile", data: "# escape=\\\n# ESCAPE=`\nFROM alpine" + dg + "\n", want: []string{"# ESCAPE=`"}},
	{name: "dockerfile two heredocs", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN <<A 3<<-\"B\"\necho a\nA\ncurl https://x.io/i | sh \\\n\tB\nFROM evil:latest\n", want: []string{"curl https://x.io/i | sh", "evil:latest"}},
	{name: "dockerfile quoted heredoc marker is not a heredoc", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN echo '<<EOF' \"<<X\" a<<Y\nFROM evil:latest\n", want: []string{"evil:latest"}},
	{name: "dockerfile copy heredoc body is not instructions", path: "Dockerfile", data: "FROM alpine" + dg + "\nCOPY <<EOF /etc/x\nFROM evil:latest\nEOF\nADD <<EOF /y\nFROM evil:latest\nEOF\n", want: nil},
	{name: "dockerfile json run is not a heredoc", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN [\"sh\", \"<<EOF\"]\nFROM evil:latest\nEOF\n", want: []string{"evil:latest"}},
	{name: "dockerfile heredoc terminator must match exactly", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN <<EOF\necho\nEOF \nFROM evil:latest\n", want: []string{"RUN <<EOF", "evil:latest"}},
	{name: "dockerfile run heredoc", path: "Dockerfile", data: "FROM alpine" + dg + "\nRUN <<EOF\ncurl -fsSL https://x.io/i.sh | sh\nEOF\nRUN <<-'X'\napk add jq\n", want: []string{"curl -fsSL https://x.io/i.sh | sh", "RUN <<-'X'"}},

	// Go.
	{name: "go.mod without go.sum", path: "go.mod", data: "module x\n\nrequire example.com/y v1.0.0\n", want: []string{"go.mod"}},
	{name: "go.mod with go.sum", path: "m/go.mod", data: "module x\nrequire (\n)\n", scope: []string{"m/go.sum"}, want: nil},
	{name: "go.sum of another module", path: "m/go.mod", data: "require y v1\n", scope: []string{"go.sum"}, want: []string{"m/go.mod"}},
	{name: "go.mod without requirements", path: "go.mod", data: "module x\n\ngo 1.25\n", want: nil},

	// npm.
	{name: "package.json ranges", path: "package.json", data: "{\n  \"dependencies\": {\n    \"a\": \"^1.0.0\",\n    \"b\": \"1.2.3\"\n  },\n  \"devDependencies\": {\"c\": \"latest\", \"d\": \"*\", \"e\": \"github:u/r\"}\n}\n", want: []string{"a@^1.0.0", "c@latest", "d@*", "e@github:u/r"}},
	{name: "package.json exact, no lockfile", path: "package.json", data: `{"dependencies": {"a": "1.2.3", "w": "workspace:*", "f": "file:../f", "g": "git+https://x/y#` + sha + `", "n": "npm:z@2.0.0"}}`, want: []string{"package.json"}},
	{name: "package.json with lockfile", path: "web/package.json", data: `{"dependencies": {"a": "^1"}}`, scope: []string{"web/package-lock.json"}, want: nil},
	{name: "package.json workspace lockfile", path: "pkgs/a/package.json", data: `{"dependencies": {"a": "^1"}}`, scope: []string{"pnpm-lock.yaml"}, want: nil},
	{name: "package.json without dependencies", path: "package.json", data: `{"name": "x", "dependencies": "bad"}`, want: nil},
	{name: "package.json invalid", path: "package.json", data: `{`, want: []string{"package.json"}},

	// Python.
	{name: "requirements unpinned", path: "requirements.txt", data: "# c\nflask>=2\nrequests\n-r base.txt\n--index-url https://x\ndjango==4.*\nidna==3.7 ; python_version > '3'\nurllib3 == 2.2.1  # ok\n-e .\n-e git+https://g/x@main#egg=x\nx @ git+https://g/x@" + sha + "\ny @ https://h/y.tar.gz#sha256=ab\nz @ https://h/z.tar.gz\n-e\n./local\n", want: []string{"flask>=2", "requests", "base.txt", "django==4.*", "git+https://g/x@main#egg=x", "z@https://h/z.tar.gz"}},
	{name: "requirements includes", path: "ci/requirements.txt", data: "-r base.txt\n-c ../constraints.txt\n--requirement=dev.txt\n-rother.txt\n--constraint x.txt\n--requirements-y\n--constraint=\n-r\n", scope: []string{"ci/base.txt", "constraints.txt"}, want: []string{"dev.txt", "other.txt", "x.txt", "--constraint="}},
	{name: "requirements with hashes", path: "dev-requirements.txt", data: "a==1.0 \\\n    --hash=sha256:ab\nb==2.0\n./local\n", want: []string{"b==2.0"}},
	{name: "requirements range with ==", path: "requirements-dev.txt", data: "a==1.0,<2\n", want: []string{"a==1.0,<2"}},
	{name: "requirements directory", path: "requirements/dev.txt", data: "requests>=2\n", want: []string{"requests>=2"}},
	{name: "poetry without lock", path: "pyproject.toml", data: "[tool.poetry]\nname='x'\n", want: []string{"pyproject.toml"}},
	{name: "poetry with lock", path: "pyproject.toml", data: "[tool.poetry]\n", scope: []string{"poetry.lock"}, want: nil},
	{name: "pyproject without dependencies", path: "pyproject.toml", data: "[project]\nname = 'x'\ndependencies = []\n", want: nil},
	{name: "pyproject dependencies without lock", path: "pyproject.toml", data: "[project]\ndependencies = [\n  \"requests>=2\",\n]\n", want: []string{"pyproject.toml"}},
	{name: "pyproject optional dependencies without lock", path: "pyproject.toml", data: "[project]\n[project.optional-dependencies]\ntest = ['pytest']\n", want: []string{"pyproject.toml"}},
	{name: "pyproject dependencies with uv.lock", path: "pkgs/a/pyproject.toml", data: "[project]\ndependencies = [\"requests>=2\"]\n", scope: []string{"uv.lock"}, want: nil},
	{name: "pipfile without lock", path: "Pipfile", data: "[packages]\n", want: []string{"Pipfile"}},
	{name: "pipfile with lock", path: "Pipfile", data: "[packages]\n", scope: []string{"Pipfile.lock"}, want: nil},

	// Rust.
	{name: "cargo without lock", path: "Cargo.toml", data: "[package]\n[dependencies]\nserde = \"1\"\n", want: []string{"Cargo.toml"}},
	{name: "cargo target deps", path: "Cargo.toml", data: "[target.'cfg(unix)'.dev-dependencies]\n", want: []string{"Cargo.toml"}},
	{name: "cargo workspace lock", path: "crates/a/Cargo.toml", data: "[dependencies]\n", scope: []string{"Cargo.lock"}, want: nil},
	{name: "lockfile search ends at the root", path: "/w/Cargo.toml", data: "[dependencies]\n", want: []string{"/w/Cargo.toml"}},
	{name: "cargo without dependencies", path: "Cargo.toml", data: "[package]\nname = \"x\"\n", want: nil},

	// Java.
	{name: "maven moving versions", path: "pom.xml", data: "<modelVersion>4.0.0</modelVersion>\n<version>1.0-SNAPSHOT</version>\n<junit.version>LATEST</junit.version> <version>[1.0,2.0)</version>\n<version>RELEASE</version>\n<version>${junit.version}</version>\n<version>5.10.2</version>\n",
		want: []string{"<version>1.0-SNAPSHOT</", "<junit.version>LATEST</", "<version>[1.0,2.0)</", "<version>RELEASE</"}},
	{name: "gradle moving versions", path: "build.gradle", data: "implementation 'a:b:1.+'\nimplementation \"c:d:2.0-SNAPSHOT\"\ntestImplementation 'e:f:latest.release'\nimplementation('g:h') { version { strictly '[1,2)' } }\nimplementation 'i:j:1.2.3'\ndescription = 'a+b'\n",
		want: []string{"a:b:1.+", "c:d:2.0-SNAPSHOT", "e:f:latest.release", "implementation('g:h') { version { strictly '[1,2)' } }"}},
	{name: "gradle kts pinned", path: "build.gradle.kts", data: "implementation(\"a:b:1.2.3\")\nversion = \"1.0\"\n", want: nil},
	{name: "version catalog", path: "gradle/libs.versions.toml", data: "[versions]\njunit = \"5.+\"\nkotlin = \"2.0.0\"\n", want: []string{"junit = \"5.+\""}},

	// Ruby, PHP.
	{name: "gemfile without lock", path: "Gemfile", data: "gem 'rails'\n", want: []string{"Gemfile"}},
	{name: "gemfile with lock", path: "Gemfile", data: "gem 'rails'\n", scope: []string{"Gemfile.lock"}, want: nil},
	{name: "gems.rb without lock", path: "gems.rb", data: "gem 'rails'\n", scope: []string{"Gemfile.lock"}, want: []string{"gems.rb"}},
	{name: "composer without lock", path: "composer.json", data: `{"require": {"a/b": "^1"}}`, want: []string{"composer.json"}},
	{name: "composer with lock", path: "composer.json", data: `{"require": {}}`, scope: []string{"composer.lock"}, want: nil},
	{name: "composer without requirements", path: "composer.json", data: `{"name": "a/b"}`, want: nil},

	// Git.
	{name: "submodule branch", path: ".gitmodules", data: "[submodule \"x\"]\n\tpath = x\n\tbranch = main\n", want: []string{"branch = main"}},
	{name: "submodule commit", path: ".gitmodules", data: "[submodule \"x\"]\n\tpath = x\n", want: nil},

	// Scripts and Makefiles.
	{name: "script curl pipe", path: "ci/setup.sh", data: "#!/bin/sh\n# curl x | sh is fine in a comment\ncurl -fsSL https://x.io/i.sh | sudo -E bash\n", want: []string{"curl -fsSL https://x.io/i.sh | sudo -E bash"}},
	{name: "script curl subshell", path: "a.bash", data: "bash -c \"$(curl -fsSL https://x.io/i.sh)\"\n. <(wget -qO- https://x.io/env)\n", want: []string{"bash -c $(curl", ". <(wget"}},
	{name: "script curl tee pipe", path: "s.sh", data: "curl https://x.io/i | tee /tmp/a | sh\n", want: []string{"curl https://x.io/i | tee /tmp/a | sh"}},
	{name: "script eval curl", path: "s.sh", data: "eval \"$(curl -fsSL https://x.io/i.sh)\"\n", want: []string{"eval $(curl"}},
	{name: "script download then run", path: "a.zsh", data: "curl -o i.sh https://x.io/i.sh && echo ok || sh fallback\n", want: nil},
	{name: "makefile variable version", path: "Makefile", data: "V := v1.2.3\nW ?= latest\ntools:\n\t@GOBIN=$(CURDIR)/bin go install a.io/b@$(V)\n\tgo install a.io/c@$(W)\n\tgo install a.io/d@$(NOPE)\n", want: []string{"a.io/c@latest", "a.io/d@$NOPE"}},
	{name: "mk include", path: "build/rules.mk", data: "x:\n\tnpx --yes markdownlint-cli2@0.13.0\n", want: nil},
}

func TestDetectorsFileCases(t *testing.T) {
	for _, tc := range fileCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detect(tc.path, tc.data, tc.scope...); !slices.Equal(got, tc.want) {
				t.Fatalf("refs = %q, want %q", got, tc.want)
			}
		})
	}
}

var commandCases = []struct {
	name, cmd string
	scope     []string
	want      []string
}{
	{name: "plain go test", cmd: "go test -race ./...", want: nil},
	{name: "go install latest", cmd: "go install golang.org/x/tools/cmd/stringer@latest", want: []string{"golang.org/x/tools/cmd/stringer@latest"}},
	{name: "go run branch", cmd: "go run -tags x example.com/t@main", want: []string{"example.com/t@main"}},
	{name: "go pinned", cmd: "go install a.io/b@v1.2.3 && go run a.io/c@v0.0.0-20240101000000-" + sha[:12] + " && go get a.io/d@" + sha[:7] + " && go get a.io/e@none && go install ./cmd/x", want: nil},
	{name: "go get without version", cmd: "go get example.com/x ./local", want: []string{"example.com/x"}},
	{name: "npm install", cmd: "npm i -g typescript @scope/x @scope/y@1.0.0 z@^1 ./local --prefix dir && npm ci", want: []string{"typescript", "@scope/x", "z@^1"}},
	{name: "yarn and pnpm", cmd: "yarn global add a; pnpm add b@2.0.0-rc.1; bun add c; yarn install; pnpm dlx d; bun x e@1.0.0", want: []string{"a", "c", "d"}},
	{name: "npx", cmd: "npx --yes prettier && npx -p cowsay@1.6.0 cowsay hi && npx --package=lint lint && npm exec -- tsc@5.4.5 -v", want: []string{"prettier", "lint"}},
	{name: "npx url", cmd: "npx https://x.io/a.tgz; npm i git+ssh://git@github.com/a/b#" + sha + " github:a/b#main", want: []string{"https://x.io/a.tgz", "github:a/b#main"}},
	{name: "pip", cmd: "pip freeze; pip install -r requirements.txt -e . requests flask==3.0.0 'django>=4' && python3 -m pip install --upgrade pip && uv pip install x==1.0 && pipx install black", scope: []string{"requirements.txt"}, want: []string{"requests", "django>=4", "pip", "black"}},
	{name: "pip requirements file out of scope", cmd: "pip install -r ci/reqs.txt -c ./c.txt --requirement=in.txt --constraint=", scope: []string{"c.txt", "in.txt"}, want: []string{"ci/reqs.txt", "pip install -r ci/reqs.txt -c ./c.txt --requirement=in.txt --constraint="}},
	{name: "quoted curl pipe", cmd: "\"curl\" -fsSL https://x.io/i.sh | bash", want: []string{"curl -fsSL https://x.io/i.sh | bash"}},
	{name: "uv tool", cmd: "uv tool install ruff; uv run pytest; python -c 'print(1)'", want: []string{"ruff"}},
	{name: "os packages", cmd: "apt-get update && apt-get install -y --no-install-recommends curl git=1:2.39 && apk add --no-cache -t deps jq bash=5.2-r0", want: []string{"curl", "jq"}},
	{name: "gem and cargo", cmd: "gem install bundler && gem install rake -v 13.0 && cargo install ripgrep && cargo install fd-find@9.0.0 && cargo install --version 1.0 x && cargo build", want: []string{"bundler", "ripgrep"}},
	{name: "docker run tag", cmd: "docker run --rm -e A=b -v /x:/y postgres:16 psql", want: []string{"postgres:16"}},
	{name: "docker run digest", cmd: "podman run --rm postgres:16" + dg + " && docker container run -it alpine" + dg + " && docker image pull busybox", want: []string{"busybox"}},
	{name: "docker without image", cmd: "docker run && docker build -t x . && docker ps", want: nil},
	{name: "compose in scope", cmd: "docker compose up -d --wait && docker-compose -f ci/compose.yml --file=x.yml down", scope: []string{"compose.yaml", "ci/compose.yml", "x.yml"}, want: nil},
	{name: "compose not in scope", cmd: "docker compose -f other.yml up; docker compose down", scope: []string{"x"}, want: []string{"other.yml", "compose.yaml"}},
	{name: "git clone", cmd: "git clone --depth 1 https://g/x", want: []string{"https://g/x"}},
	{name: "git clone at a commit", cmd: "git clone https://g/y && git -C y checkout " + sha, want: nil},
	{name: "git clone bare", cmd: "git clone", want: []string{"git clone"}},
	{name: "git submodule remote", cmd: "git submodule update --init --remote; git submodule update --init", want: []string{"git submodule update --remote"}},
	{name: "sh -c wrapper", cmd: "sh -c REDIS=1 sudo env -i X=1 nohup go install a.io/b@master", want: []string{"a.io/b@master"}},
	{name: "conditional", cmd: "if true; then npm i left-pad; fi", want: []string{"left-pad"}},
	{name: "redirections", cmd: "pip install 'requests>=2' > log 2>err && npm i x@1.0.0 >>log </dev/null", want: []string{"requests>=2"}},
	{name: "only assignments", cmd: "A=1 B=2", want: nil},
	{name: "curl pipe python", cmd: "wget -qO- https://x.io/get.py | python3 -", want: []string{"wget -qO- https://x.io/get.py | python3"}},
	{name: "curl query string pipe", cmd: `curl -fsSL "https://x.io/i?a=1&b=2" | bash`, want: []string{"curl -fsSL https://x.io/i?a=1&b=2 | bash"}},
	{name: "curl escaped ampersand pipe", cmd: `curl https://x.io/i?a=1\&b=2 | sh`, want: []string{`curl https://x.io/i?a=1\&b=2 | sh`}},
	{name: "curl pipe env bash", cmd: "wget -qO- https://x.io/i | /usr/bin/env bash\ncurl https://x.io/j | env -i sh", want: []string{"wget -qO- https://x.io/i | /usr/bin/env bash", "curl https://x.io/j | env -i sh"}},
	{name: "global options before the subcommand", cmd: "apk --no-cache add curl; apt-get -qq install -y jq; apt-get -o Dpkg::Use-Pty=0 install git", want: []string{"curl", "jq", "git"}},
	{name: "pip global options", cmd: "pip --isolated install requests; python -m pip --disable-pip-version-check install flask; pip --cache-dir /c install a==1.0", want: []string{"requests", "flask"}},
	{name: "npm docker git global options", cmd: "npm --prefix web install left-pad; docker --context ci run redis:7; git -C deps clone https://g/x; git -c a=b clone https://g/z", want: []string{"left-pad", "redis:7", "https://g/x", "https://g/z"}},
	{name: "global option without a subcommand", cmd: "apk --version; git --no-pager", want: nil},
	{name: "git clone commit elsewhere on the line", cmd: "git clone https://g/x; echo " + sha, want: []string{"https://g/x"}},
	{name: "two clones one checkout", cmd: "git clone https://g/a && git clone https://g/b && git -C b checkout " + sha + "", want: []string{"https://g/a", "https://g/b"}},
	{name: "git pull", cmd: "git -C x pull --ff-only; git fetch", want: []string{"git pull"}},
	{name: "rpm packages", cmd: "dnf -y install curl curl-7.76.1-26.el9 java-17-openjdk && yum install ./x.rpm && microdnf install jq && zypper -n in git vim=9.0 && dnf update", want: []string{"curl", "java-17-openjdk", "jq", "git"}},
	{name: "releases latest", cmd: "curl -fsSL https://github.com/a/b/releases/latest/download/b.tgz | tar xz; wget -qO- https://github.com/a/b/releases/download/v1.0/b.tgz | tar xz", want: []string{"curl -fsSL https://github.com/a/b/releases/latest/download/b.tgz"}},
	{name: "python -c curl", cmd: `python3 -c "$(curl -fsSL https://x.io/i.py)"`, want: []string{"python3 -c $(curl"}},
	{name: "kubectl image", cmd: "kubectl run t --image=busybox:1.36 --rm; kubectl create deployment d --image nginx; kubectl run p --image=busybox" + dg + "; kubectl get pods", want: []string{"busybox:1.36", "nginx"}},
	{name: "helm chart version", cmd: "helm install r bitnami/redis; helm upgrade --install r oci://r.io/c --version 1.2.3; helm install ./charts/app --generate-name; helm install r2 c --repo https://x --version ^1; helm install r3 charts/local; helm upgrade r4 a.tgz --version=v2.0.0; helm list; helm install", scope: []string{"charts/local/Chart.yaml"}, want: []string{"bitnami/redis", "./charts/app", "c"}},
	{name: "git clone then fetch a commit", cmd: "git clone https://g/x && git -C x fetch --depth 1 origin " + sha + " && git -C x reset --hard " + sha, want: nil},
}

func TestShellCommandCases(t *testing.T) {
	for _, tc := range commandCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cmdRefs(tc.cmd, tc.scope...); !slices.Equal(got, tc.want) {
				t.Fatalf("refs = %q, want %q", got, tc.want)
			}
		})
	}
}

func write(t *testing.T, dir, name, data string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "z/compose.yaml", "services:\n  a:\n    image: b:1\n  c:\n    image: d:2\n")
	write(t, dir, "Dockerfile", "FROM a:1\nFROM a:1\n")
	write(t, dir, "main.go", "package main\n")
	write(t, dir, "big.sh", strings.Repeat("#", MaxFileSize+1))
	write(t, dir, "pnpm-lock.yaml", strings.Repeat("#", MaxFileSize+1)) // hashed, not scanned
	got := Scan(os.DirFS(dir), []string{"Dockerfile", "big.sh", "gone.sh", "main.go", "pnpm-lock.yaml", "z/compose.yaml"}, []string{"docker", "run", "e:3"})
	var lines []string
	for _, f := range got {
		lines = append(lines, f.String())
	}
	want := []string{
		"Dockerfile:1 (container) a:1: a tag can be repushed: pin the image by digest (name:tag@sha256:...)",
		"Dockerfile:2 (container) a:1: a tag can be repushed: pin the image by digest (name:tag@sha256:...)",
		"big.sh (scan) big.sh: larger than 1 MiB: Aval cannot check its references; shrink or split the file",
		"gone.sh (scan) gone.sh: open gone.sh: no such file or directory: Aval cannot check its references; shrink or split the file",
		"runner.command:1 (container) e:3: a tag can be repushed: pin the image by digest (name:tag@sha256:...)",
		"z/compose.yaml:3 (container) b:1: a tag can be repushed: pin the image by digest (name:tag@sha256:...)",
		"z/compose.yaml:5 (container) d:2: a tag can be repushed: pin the image by digest (name:tag@sha256:...)",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	allowed := Finding{File: "a", Ecosystem: "go", Ref: "r", Fix: "f", Allowed: "why"}.String()
	if allowed != "a (go) r: f (allowed: why)" {
		t.Fatal(allowed)
	}
}

func TestScanDedupesAndSkipsUnknownFiles(t *testing.T) {
	dir := t.TempDir()
	if got := Scan(os.DirFS(dir), []string{"README.md"}, []string{"docker", "run", "a:1", "&&", "docker", "run", "a:1", "&&", "docker", "compose", "up"}); len(got) != 2 {
		t.Fatalf("got %v, want the image once and the compose file", got)
	}
}

// TestScanShebangScripts: an in-scope file without an extension is scanned
// when it starts with a shell shebang; other extension-less files are not.
func TestScanShebangScripts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "setup", "#!/bin/sh\ncurl https://x.io/i | sh\n")
	write(t, dir, "bin/ci", "#!/usr/bin/env -S bash\napk --no-cache add curl\n")
	write(t, dir, "LICENSE", "curl https://x.io/i | sh\n")
	write(t, dir, "run", "#!/usr/bin/python3\nimport os\n")
	var refs []string
	for _, f := range Scan(os.DirFS(dir), []string{"LICENSE", "bin/ci", "run", "setup", "gone"}, nil) {
		refs = append(refs, f.File+" "+f.Ref)
	}
	want := []string{"bin/ci curl", "gone gone", "setup curl https://x.io/i | sh"}
	if !slices.Equal(refs, want) {
		t.Fatalf("refs = %q, want %q", refs, want)
	}
}

func TestExpandIsBounded(t *testing.T) {
	v := &vars{m: map[string]string{"A": strings.Repeat("x", MaxFileSize)}, budget: maxExpand}
	if got := v.expand("$A $A"); len(got) != 2*MaxFileSize+1 {
		t.Fatalf("expanded to %d bytes", len(got))
	}
	if got := v.expand("$A $A"); got != "$A $A" {
		t.Fatalf("expanded past the budget to %d bytes", len(got))
	}
	v = &vars{m: map[string]string{"A": "${B}", "B": "b"}, budget: maxExpand}
	if got := v.expand("$(A) ${B} $$C"); got != "b b $C" {
		t.Fatal(got)
	}
}

func TestNpmLineLookupsAreBounded(t *testing.T) {
	var deps []string
	for i := range maxLineLookups + 1 {
		deps = append(deps, fmt.Sprintf("%q: \"^1\"", fmt.Sprintf("d%03d", i)))
	}
	data := `{"dependencies": {` + strings.Join(deps, ",\n") + `}}`
	fs := npm{}.Detect(Input{Path: "package.json", Data: []byte(data), Has: func(string) bool { return false }})
	if len(fs) != maxLineLookups+1 || fs[0].Line != 1 || fs[maxLineLookups].Line != 0 {
		t.Fatalf("%d findings, first line %d, last line %d", len(fs), fs[0].Line, fs[len(fs)-1].Line)
	}
}

func TestLogicalLines(t *testing.T) {
	got := logicalLines("a \\\r\nb\r\nc\\\n")
	want := []line{{1, "a  b"}, {3, "c "}}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
	if got := logicalLines("a\nb \\"); !slices.Equal(got, []line{{1, "a"}, {2, "b  "}}) {
		t.Fatalf("a continuation at the end is dropped: %v", got)
	}
	if lineOf([]byte("a\nb"), "zz") != 0 || lineOf([]byte("a\nb"), "b") != 2 {
		t.Fatal("lineOf")
	}
}

// FuzzDetect: no detector panics on untrusted content, and every finding
// points into the file it was given.
func FuzzDetect(f *testing.F) {
	for _, tc := range fileCases {
		f.Add(tc.path, tc.data)
	}
	for _, tc := range commandCases {
		f.Add("x.sh", tc.cmd)
	}
	f.Fuzz(func(t *testing.T, name, data string) {
		n := strings.Count(data, "\n") + 1
		for _, d := range Detectors {
			for _, path := range []string{name, "Dockerfile", "compose.yaml", "package.json", "requirements.txt", "pom.xml", "build.gradle", "x.sh", "go.mod", "Cargo.toml", ".gitmodules", "kustomization.yaml", "compose.json"} {
				if !d.Applies(path) {
					continue
				}
				for _, fd := range d.Detect(Input{Path: path, Data: []byte(data), Has: func(string) bool { return false }}) {
					if fd.File != path || fd.Line < 0 || fd.Line > n || fd.Ref == "" || fd.Fix == "" {
						t.Fatalf("bad finding %+v for %d lines", fd, n)
					}
				}
			}
		}
	})
}
