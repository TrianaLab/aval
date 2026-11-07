# Maven artifacts

`maven: <group>:<artifact>:<version>` hashes the POM of that version
together with the checksums the repository publishes for its `.jar` and
its main artifact (`.sha256`, else `.sha1`).

An exact version binds that version. Anything else (a range, latest, a
tag) binds the whole release list, so any new release of the package
makes the test run again; use an exact version (or a lockfile) for stable
reuse.

```yaml
scope:
  resolve:
    - maven: org.postgresql:postgresql:42.7.4         # exact
    - maven: org.junit:junit-bom:5.+                  # floating
    - maven: org.apache.commons:commons-lang3:RELEASE # floating
```

- The group and artifact ids must be a letter, digit or `_`, then
  letters, digits, `_`, `.` or `-`, with no `..`. Anything else fails.
- Exact means a plain version: no `+`, `[`, `(` or `,`, and not `LATEST`,
  `RELEASE` or `latest.*`.
- For anything else the id is a hash of the `<versioning>` block of
  `maven-metadata.xml` (every version, `<latest>` and `<release>`) and of
  each listed version's POM and artifact checksums, as for an exact
  version. A new version, a moved `<release>` or a redeployed release
  changes the id. The manifest line says `floating`. A
  `maven-metadata.xml` without versions, or with more than 100 (up to six
  requests each), fails: use an exact version. Floating specs are
  impractical for popular artifacts (guava, jackson-databind list more
  than 200 versions).
- A version ending in `SNAPSHOT` (`1.0-SNAPSHOT`, `1.0.SNAPSHOT`), in any
  letter case, fails, and so does a floating version whose list holds
  one: a snapshot is redeployed under the same name. Resolve a release,
  or pin.
- A tool that names `group:artifact:version` downloads the `.jar`
  whatever the POM `packaging` says, so the `.jar` checksum is bound
  whenever the repository has a `.jar`. The packaging adds its own file:
  `war`, `ear`, `rar` and `aar` bind that file's checksum too. A `.jar`
  is required for `jar` (the default), `bundle`, `maven-plugin` and
  `ejb`; for `pom` (a BOM or parent) and the other packagings it is bound
  only when present. Another packaging, a missing checksum, a `.jar`
  served without one, or a checksum file that holds no checksum (a
  proxy's error page), fails the fingerprint, so the test runs. Aval
  never downloads the jar: the id is only as fresh as the repository's
  published checksum. A jar overwritten under the same version on a
  private repository changes the id only if its checksum file is
  redeployed with it. Classifier
  artifacts (`-sources`, `-linux-x86_64`) are not hashed, nor is the
  Gradle module metadata (`.module`), from which Gradle can pick another
  file: on a private repository, a redeployed `.module` does not change
  the id.
- When the repository publishes only a `.sha1`, the id rests on SHA-1.
- Repository: `$AVAL_MAVEN_URL`. Credentials: `~/.netrc`.

**Pin instead** in `pom.xml` or `build.gradle` with exact versions; Gradle
can also lock dependencies (`gradle.lockfile`).
