# Pin your dependencies

Aval reuses a result when the fingerprint of the declared inputs matches. The
fingerprint hashes the text of your files. If a file says `postgres:16`, Aval
hashes the words `postgres:16`, not the image behind them. When someone
pushes a new image under that tag, the text stays the same, the fingerprint
stays the same, and without a check Aval would reuse a result that was
produced with a different image.

A reference like that is **mutable**: its content can change while its name
stays the same. Aval looks for mutable references in the files in the
contract scope it recognizes (see each ecosystem below) and in
`runner.command`, and refuses to reuse a result that depends on one.

## What Aval does with a finding

| Cacheability | What happens |
|---|---|
| `hermetic`, `controlled-environment` | The lookup is a `miss` and the reason lists every reference. `aval run` runs the test; `aval run --push` and `aval push` do not publish the result. `aval verify` exits 4. |
| `external-dependencies` | The findings are printed as information. The class is not reusable by default anyway. |
| `non-deterministic` | Unchanged: never reusable. |

`aval run` prints one line per finding:

```text
mutable: compose.yaml:3 (container) postgres:16: a tag can be repushed: pin the image by digest (name:tag@sha256:...)
```

The line names the file and line, the ecosystem, the reference as written,
and what to pin. `aval run --fingerprint-only` prints the same lines on
stderr, so stdout stays the digest.

Detection is static and offline: Aval reads files, it never fetches, resolves
or runs anything. It reads at most 1 MiB per file; a larger file it should
check, or one it cannot read, is itself a finding. Lockfiles
(`pnpm-lock.yaml`, `go.sum`, `Cargo.lock`, ...) are hashed but not scanned.

## Fix it by ecosystem

### Container images

Pin every image by digest. Keep the tag for readers; the digest decides.

```yaml
# compose.yaml, Kubernetes manifests, rendered Helm charts
image: redis:7.4-alpine@sha256:c5d34a98396a4775e223c74e929162c35c5a2182901ba26b50ade939941ce14b
```

```dockerfile
FROM golang:1.25@sha256:<digest> AS build
FROM build AS test          # a stage name is fine
COPY --from=nginx:1.27@sha256:<digest> /etc/nginx /etc/nginx
ADD --checksum=sha256:<sum> https://example.com/tool.tgz /tool.tgz
```

A `# syntax=` parser directive names the BuildKit frontend image: pin it too
(`# syntax=docker/dockerfile:1@sha256:<digest>`).

Aval splits a Dockerfile into instructions the way BuildKit does: a comment
line stands on its own, even when it ends in `\`; a continued line joins the
next one with nothing in between (`FROM db:1\` then `6` is `db:16`); comment
and blank lines inside a continuation are dropped; and a leading
`# escape=` directive sets the continuation character. The bodies of
`COPY` and `ADD` heredocs are file content, not instructions. A Dockerfile
BuildKit refuses (an invalid `# escape=`, a repeated directive, an
unterminated heredoc) is reported in the `scan` ecosystem, and so is a
heredoc name with a quote, backslash or `$` inside it (`<<#\\x`,
`<<E$X`), or an empty one (`<<""`), which Aval does not decode. Every
`NAME=value` of an `ARG` counts (`ARG A=1 IMG=x`, also lowercase `arg`); a
`FROM` that uses an `ARG` given a value more than once is a `scan`
finding: give it one value.

A `kustomization.yaml` that retargets an image in `images:` (or the legacy
`imageTags:`) with `newName` or `newTag` needs a `digest:` too. A
`tagSuffix` is always reported: kustomize drops the digest when it appends
the suffix. A `patches`, `patchesStrategicMerge`, `patchesJson6902` or
`replacements` entry that mentions `image` is a `scan` finding, since Aval
does not apply patches: pin the image in the base, or retarget it in
`images:` with a digest.

```yaml
images:
  - name: postgres
    newTag: "16"
    digest: sha256:<digest>
```

Aval reads `images:` as kustomize does: field names match case-insensitively
(`IMAGES:`, `NewTag:`) and merge keys (`<<:`) are resolved. A kustomization
that is not valid YAML, or whose `images:` kustomize could not decode, is
reported in the `scan` ecosystem.

Find the digest with `docker buildx imagetools inspect <image:tag>` or
`crane digest <image:tag>`. Images in `docker run`, `podman run` and
`docker compose` commands, and `kubectl run`/`create --image`, follow the
same rule. A compose file the command
uses must be in `scope.source`, so its images get checked too; a compose
file can be YAML or JSON (`compose.json`). An image
that comes from a variable Aval cannot resolve (`${DB_IMAGE}`) or from a
Helm template (`{{ .Values.image }}`) is reported: write the digest into a
file in the scope, or render the chart into the scope. A YAML alias
(`image: *img`), a block scalar (`image: >-`) or a tag (`image: !!str ...`)
is reported too: write the reference inline. A file that is valid YAML is
read by the YAML parser alone: every `image:` key is matched by its decoded
value (`"ima\x67e":`, `? image`, `!!str image:`, anchored keys), wherever
its value is written. A Compose `build.dockerfile_inline` is checked as a
Dockerfile and `build.additional_contexts` values with `docker-image://` as
images. Helm helpers (`*.tpl`) are checked like templates. A key a template
writes (`ima{{ "ge" }}:`, `{{ $k }}:`) is reported in the `scan` ecosystem.
A file that is not valid YAML (a Helm template) is read line by line: only
an escaped key with its value on the same line is matched by its decoded
value; a quoted key with an escape YAML cannot decode, an escaped `image`
key whose value is elsewhere (`? "\x69mage"`, a value on the next line)
and a quoted key that continues on the next line are reported in the
`scan` ecosystem.

### Go

Commit `go.sum` and add it to `scope.dependencies`. It pins every module by
hash. In commands, use release versions:

```sh
go install golang.org/x/tools/cmd/stringer@v0.30.0   # not @latest or @main
```

### JavaScript: npm, yarn, pnpm, bun

Commit the lockfile (`package-lock.json`, `yarn.lock`, `pnpm-lock.yaml` or
`bun.lock`) and add it to `scope.dependencies`. Without one, every range
(`^1.2.0`, `latest`, `*`) is reported. Install from the lockfile and pin what
you run:

```sh
npm ci
npx --yes prettier@3.3.3
```

### Python

Pin every requirement with `==` and, better, with hashes. Generate the file
instead of writing it by hand:

```sh
uv pip compile --generate-hashes requirements.in -o requirements.txt
pip install --require-hashes -r requirements.txt
```

If a requirements file uses hashes, every entry needs one. Poetry needs
`poetry.lock` and Pipenv needs `Pipfile.lock` next to the manifest. A
`pyproject.toml` with `[project]` dependencies needs `uv.lock`, `pdm.lock` or
`poetry.lock`, next to it or at the workspace root. A git
requirement needs a full commit id: `pkg @ git+https://host/repo@<40-hex>`.
Any `requirements*.txt` path is checked, also `requirements/dev.txt`. A file
named by `pip install -r` or `-c` must be in the scope, so its pins get
checked; otherwise it is reported. The same holds for a `-r`/`-c` include
line inside a requirements file (resolved next to that file).

### Rust

Commit `Cargo.lock` (at the workspace root) and add it to
`scope.dependencies`. In commands, use `cargo install tool --version 1.2.3`.

### Java: Maven and Gradle

Use release versions. `1.0-SNAPSHOT`, `LATEST`, `RELEASE`, dynamic versions
(`1.+`, `latest.release`) and ranges (`[1.0,2.0)`) are reported in
`pom.xml`, `build.gradle`, `build.gradle.kts` and `*.versions.toml`.

### Ruby and PHP

Commit `Gemfile.lock` (or `gems.locked`) and `composer.lock`, and add them to
`scope.dependencies`. In commands, use `gem install tool --version 1.2.3`.

### OS packages

`apt-get install`, `apk add`, `dnf`/`yum`/`microdnf install` and
`zypper install` install whatever the mirror has today. Pin versions
(`curl=8.5.0-2`, `jq=1.7.1-r0`, `curl-7.76.1-26.el9`, `vim=9.0`) or, better,
install the packages into an image and pin that image by digest.

### Helm

`helm install` and `helm upgrade` of a chart that is not in the scope need an
exact `--version 1.2.3`. A chart directory in the scope (its `Chart.yaml`)
is checked as files.

### Scripts from the network

`curl … | sh` (also through `tee` or other pipe stages), `bash -c "$(curl …)"`,
`python -c "$(curl …)"`, `eval "$(curl …)"` and `source <(wget …)` run
whatever the server sends. A `curl` or `wget` of a `releases/latest` URL
fetches whatever release is newest. Dockerfile `RUN` instructions, including BuildKit heredoc
bodies (`RUN <<EOF`), get the same shell checks. Vendor the script into the scope, or download a fixed version
and check its sha256 before running it.

Aval finds scripts by name: `*.sh`, `*.bash`, `*.zsh`, `*.mk`, `Makefile`,
and in-scope files without an extension whose first line is a `sh`, `bash`,
`zsh`, `ksh` or `dash` shebang (`./setup`). A script with another extension
is hashed but not scanned.

### Git

A `git clone` follows a branch or tag that can move: check out a full commit
id in the same command (`git clone URL && git -C dir checkout <40-hex>`),
one checkout per clone. A `git pull` is reported for the same reason. A
submodule with a `branch =` line in `.gitmodules`, or `git submodule update
--remote`, follows a branch: rely on the commit recorded in the repository.

## Resolve a reference instead

To follow a tag, a range or a branch on purpose, declare the reference in
`scope.resolve`: Aval resolves it to its digest, checksum or commit every
time it fingerprints and hashes that id, so a move changes the
fingerprint. It never clears a finding: the reference still blocks reuse
until you pin it in place or accept it below. `scope.resolve` binds the artifact you name, not its dependencies:
use a lockfile for those. See [Resolve references](resolve/index.md).

## Accept a reference on purpose

Sometimes a mutable reference is acceptable, for example an image your team
rebuilds and scans every night. Acknowledge it in the contract, with the
reason:

```yaml
scope:
  source: ["**/*.go", "compose.yaml"]
  allowMutable:
    - ref: postgres:16          # exactly as Aval prints it
      reason: rebuilt and scanned nightly; the tag is ours
```

`ref` must match the reference text Aval prints (for a missing lockfile, the
manifest path, such as `go.mod`). Aval drops quotes from references it finds
in shell commands, so `"curl" -fsSL URL | bash` prints as
`curl -fsSL URL | bash`. The acknowledgement is part of the
contract, so adding or removing one changes the fingerprint, and reviewers
see it in the diff. `aval run` still prints the finding, marked
`(allowed: <reason>)`. A file Aval cannot read (larger than 1 MiB, or
unreadable) is reported in the `scan` ecosystem and cannot be accepted:
shrink or split it. The same holds for the other `scan` findings above (a
Dockerfile BuildKit refuses, an undecodable YAML key): fix the file.

## Limits

The scanner is a best-effort safety net, not a guarantee. The guarantee is
the declared scope and the pins you write in place: a digest, a lockfile,
a commit id. The detectors read text; they do not run your build, render
your chart or apply your overlays, and a reference they do not recognize is
not reported. Keep the scope honest, pin in place, and use
`aval run --shadow` to catch false hits. Known limits:

- Shell commands are split without a full shell parser: a quoted `;` or
  `>` also splits, and a token misread as a package is reported as
  unpinned (a needless miss). An option Aval does not know to take a value
  (`tool --opt value install x`) hides the subcommand.
- A download from an unversioned URL other than `releases/latest`
  (`curl -o tool URL`, `curl URL | tar x`) is not detected.
- Helm: values that split an image into `repository` and `tag`, and any
  YAML a template prints (`{{ include ... }}`, `{{ toYaml ... }}`), are not
  read. In a template the YAML parser rejects, keys written as block
  scalars or aliases are not decoded.
- Kustomize: a patch file referenced by `path:` that sets an image without
  `image` in the entry is not read.
- Dockerfile variables: the last value of a name wins, with no stage
  scoping, except in `FROM` (above).

Declare what the scanner cannot see in `scope.resolve` (a
[`url`](resolve/urls.md), an [`image`](resolve/images.md)), so its content
is hashed.
