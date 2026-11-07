# Container images

`image: <name>:<tag>` resolves the tag to the digest of its manifest. For a
multi-arch image that is the digest of the index, so a new build for any
platform changes it.

```yaml
scope:
  source: ["compose.yaml", "**/*.go"]
  resolve:
    - image: postgres:16                     # Docker Hub
    - image: ghcr.io/acme/orders-db:nightly  # any OCI registry
```

- A name without a host is a Docker Hub name: `postgres:16` is
  `docker.io/library/postgres:16`. No tag means `latest`.
- The [scanner](../pinning.md#container-images) still reports the tag:
  `scope.resolve` never clears a finding. Pin by digest, or accept the
  tag with `scope.allowMutable`. The entry makes a repushed tag change the
  fingerprint.
- Compose without `pull_policy: always`, or Kubernetes with
  `IfNotPresent`, can run a copy of the tag pulled earlier. The digest Aval
  reads is the registry's, not the runner's: pull before the test, or pin
  by digest.
- Also covers what the scanner cannot see, such as Helm values that split
  `repository` and `tag`: declare the image those values make.
- Credentials come from `docker login` (`$DOCKER_CONFIG`). A loopback
  registry (`localhost:5000`) is reached over plain HTTP.

**Pin instead** when you want what you run to be exactly what you hashed:
`image: postgres:16@sha256:<digest>` in the file. Find the digest with
`crane digest postgres:16`.
