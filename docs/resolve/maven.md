# Maven artifacts

`maven: <group>:<artifact>:<version>` hashes the POM of that version
together with the checksums the repository publishes for its artifacts
(`.sha256`, else `.sha1`).

--8<-- "docs/resolve/index.md:floating"

```yaml
scope:
  resolve:
    - maven: org.postgresql:postgresql:42.7.4         # exact
    - maven: org.junit:junit-bom:5.+                  # floating
    - maven: org.apache.commons:commons-lang3:RELEASE # floating
```

- Group and artifact ids: a letter, digit or `_`, then letters, digits, `_`,
  `.` or `-`, with no `..`.
- Exact means a plain version: no `+`, `[`, `(` or `,`, and not `LATEST`,
  `RELEASE` or `latest.*`.
- Floating: the `<versioning>` block of `maven-metadata.xml` and each listed
  version's POM and checksums. No versions, more than 100 (popular artifacts
  such as guava list more than 200), or a `<latest>`/`<release>` that is a
  SNAPSHOT or not listed fails: use an exact version.
- A `SNAPSHOT` version, in any case, fails, and so does a floating list that
  holds one: a snapshot is redeployed under the same name.

| Packaging | Bound |
|---|---|
| `jar` (default), `bundle`, `maven-plugin`, `ejb` | POM and `.jar` checksum (required) |
| `war`, `ear`, `rar`, `aar` | POM, that file's checksum, and the `.jar` checksum if present |
| `pom` (a BOM or parent) | POM, and the `.jar` checksum if present |

A missing checksum, a checksum file that holds none (a proxy's error page) or
another packaging fails. Aval never downloads the jar. Not covered:

- classifier artifacts (`-sources`, `-linux-x86_64`)
- Gradle module metadata (`.module`), from which Gradle can pick another file
- a jar overwritten on a private repository without redeploying its checksum
- a repository that publishes only `.sha1`: the id rests on SHA-1

Repository `$AVAL_MAVEN_URL`; credentials from `~/.netrc`.

**Pin instead** in `pom.xml` or `build.gradle` with exact versions; Gradle
can also lock dependencies (`gradle.lockfile`).
