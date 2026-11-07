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
  `docker.io/library/postgres:16`, and so are `docker.io/postgres:16` and
  `index.docker.io/postgres:16`. No tag means `latest`.
- The digest Aval reads is the registry's, not the runner's. Compose without
  `pull_policy: always`, or Kubernetes with `IfNotPresent`, can run a copy
  pulled earlier: pull before the test, or pin by digest.
- Also covers what the [scanner](../pinning.md#container-images) cannot see,
  such as Helm values that split `repository` and `tag`: declare the image
  those values make.
- Credentials come from `docker login` (`$DOCKER_CONFIG`). A loopback
  registry (`localhost:5000`) is reached over plain HTTP.

--8<-- "docs/resolve/index.md:scanner"

**Pin instead** when you want what you run to be exactly what you hashed:
`image: postgres:16@sha256:<digest>` in the file. Find the digest with
`crane digest postgres:16`.
