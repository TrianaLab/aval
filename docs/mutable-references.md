# Mutable reference scan

Which references does Aval report, and what can it miss? The full rules
behind [Pin your dependencies](pinning.md).

## How the scan works

- It reads files only; it never fetches, resolves or runs anything.
- At most 1 MiB per file. A bigger or unreadable file it should check is a
  `scan` finding that cannot be accepted: shrink or split it.
- Lockfiles (`pnpm-lock.yaml`, `go.sum`, `Cargo.lock`, ...) are hashed,
  not scanned.

## Python

- Generate the file: `uv pip compile --generate-hashes requirements.in -o
  requirements.txt`, then `pip install --require-hashes -r
  requirements.txt`.
- Once one entry of a requirements file has a hash, every entry needs one.
- Any `requirements*.txt` path is checked, also `requirements/dev.txt`.
- Poetry needs `poetry.lock` and Pipenv `Pipfile.lock` next to the
  manifest. A `pyproject.toml` with `[project]` dependencies needs
  `uv.lock`, `pdm.lock` or `poetry.lock`, next to it or at the workspace
  root.
- A file named by `pip install -r`/`-c`, or by an include line inside a
  requirements file (resolved next to that file), must be in the scope,
  or it is reported.

## Container images

- Images in test code: in `*.go`, `*.py`, `*.java`, `*.kt`, `*.ts`,
  `*.js` and `*.cs` files that mention testcontainers, a quoted
  `name:tag` without a digest (`PostgresContainer("postgres:16")`). A
  `localhost`, dotted or 4+ digit `host:port` literal
  (`"localhost:5432"`, `"127.0.0.1:8080"`, `"db:5432"`), a clock time
  (`"12:30"`), a `sha256:` digest, a literal with a space or a scheme,
  and a name without a tag are not reported. Any other quoted
  `name:tag`, such as `"classpath:schema.sql"`, is reported and can be
  accepted in `scope.allowMutable`; an image whose tag is 4 or more
  digits without a registry (`"app:2024"`) is taken for a `host:port`
  and not reported.
- Images in `docker run`, `podman run`, `docker compose` and
  `kubectl run`/`create --image` commands. A compose file the command uses
  must be in `scope.source`; it can be YAML or JSON (`compose.json`).
- An image from a variable Aval cannot resolve (`${DB_IMAGE}`), a Helm
  template (`{{ .Values.image }}`), a YAML alias, block scalar or tag
  (`image: *img`, `image: >-`, `image: !!str ...`) is reported: write the
  reference inline, or render the chart into the scope.
- **Dockerfiles** are read twice. The line pass reads each line as an
  instruction (continuations joined with a space, `RUN` heredocs as
  scripts). The BuildKit pass splits the file as BuildKit does, for files
  named `Dockerfile`, `Dockerfile.*`, `*.dockerfile`, `Dockerfile` plus a
  name without a dot (`Dockerfile-dev`), and the same with
  `Containerfile`: comment lines stand alone, continuations join with
  nothing between (`FROM db:1\` then `6` is `db:16`), `# escape=` sets the
  escape character (continuations and heredoc words), heredoc bodies are
  kept apart.
- Both passes treat `COPY --from=` and `RUN --mount=...,from=` like a
  `FROM`: a stage name or index is fine, an image needs a digest. An
  instruction behind `ONBUILD` is checked like the same one without it.
- The BuildKit pass also reports a file BuildKit refuses (an invalid
  `# escape=`, a repeated directive, an unterminated heredoc), a heredoc
  name with a quote, `\` or `$`, or an empty one, and a `FROM` image with a
  variable that is not a global `ARG` with a value (`FROM alpine:$V`):
  give the global `ARG` a default, or write the image.
- **Kustomize**: an `images:` (or `imageTags:`) entry with `newName` or
  `newTag` needs a `digest:`. Reported by name and index
  (`resources[2]`): a remote or out-of-scope `resources`, `components` or
  `bases` entry (vendor it); a patch or `replacements` entry that sets an
  image, or is given by an out-of-scope path; a `transformers` or
  `generators` entry with an image transformer or an out-of-scope path; a
  `helmCharts` entry, unless the chart is local (no `repo`, `Chart.yaml` in
  the scope under `helmGlobals.chartHome`, default `charts`, inside the
  kustomization directory) and no `valuesInline` sets an image; any
  `tagSuffix` (kustomize drops the digest). An undecodable kustomization
  is reported by its path. The overlay is not built.
- **YAML** is read twice: the line pass matches `image:` on every line,
  also inside a block scalar a template renders; the parser matches every
  key that decodes to `image` (`"ima\x67e":`, `? image`, `!!binary
  aW1hZ2U=:`). A key with another tag, or one the parser cannot decode in
  a file it rejects, is reported; in `*.tpl` files, files under
  `templates/` and rejected files, a key a template writes
  (`ima{{ "ge" }}:`) is reported by its text, except a `range` key
  variable alone (`{{ $key | quote }}:`) directly under `labels:` or
  `annotations:`. A Compose
  `build.dockerfile_inline` is checked as a Dockerfile, and
  `build.additional_contexts` values with `docker-image://` as images.
- **JSON** is read the same way, but only in Compose files, files under
  `templates/` and files a kustomization in scope lists in `resources`.

## Scripts

`*.sh`, `*.bash`, `*.zsh`, `*.mk`, `Makefile`, and in-scope files without
an extension whose first line is a `sh`, `bash`, `zsh`, `ksh` or `dash`
shebang (`./setup`). A script with another extension is hashed but not
scanned.

## `scope.allowMutable`

- `ref` is the text Aval prints: for a missing lockfile, the manifest path
  (`go.mod`); quotes in shell commands are dropped. A finding Aval reports
  because it cannot tell (a templated key, `resources[2]`, a heredoc name, a
  `FROM` line) is in the `container` ecosystem, accepted by its printed `ref`.
- `file` is the path Aval prints before the line number, relative to the
  contract (`runner.command` for the command), and limits the entry to that
  file. Without it, a `ref` that two files report (`resources[0]` repeats) is
  not accepted, and the finding asks for `file`. With
  [`scope.contracts`](guides/depend-on-a-contract.md), each contract's entries
  apply to its own findings.
- It is part of the contract, so it changes the fingerprint and shows in
  review.
- `aval run` still prints the finding, marked `(allowed: <reason>)`.

## Limits

The scanner reads text and is a safety net, not a guarantee; the
guarantee is the declared scope and the pins you write.

- Not detected: unversioned downloads other than `releases/latest`
  (`curl -o tool URL`, `curl URL | tar x`); Helm values split into
  `repository` and `tag`, or YAML a template prints; a JSON6902 patch file
  in the scope named by `path:`; JSON outside the files above; `--build-arg` values
  (the `ARG` default is checked; outside `FROM` the last value of a name
  wins); the BuildKit pass on a source file named like
  `dockerfile_test.go`.
- In test code: an image in a variable built from parts, in a constant
  or helper in a file that does not mention testcontainers, a
  module's default image (`PostgresContainer()` without an image), the
  Ryuk reaper image, source files larger than 1 MiB or unreadable, and
  source in other languages or extensions (`.rs`, `.mjs`, `.tsx`,
  `.groovy`, `.scala`). Write the image with its digest
  (`"postgres:16@sha256:<digest>"`), or declare it in `scope.resolve`.
- Shell commands are split without a full parser: a quoted `;` or `>`
  also splits, which can cause needless misses.
- An option Aval does not know to take a value
  (`tool --opt value install x`) hides the subcommand.
- For the gaps, declare the reference in `scope.resolve`
  ([`url`](resolve/urls.md), [`image`](resolve/images.md)) and run
  `aval run --shadow`.
