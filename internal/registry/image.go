package registry

import (
	"context"
	"strings"
)

// ResolveImage returns the manifest digest a container image reference
// (name:tag, name@digest, host:port/name:tag) points to: for a multi-arch
// image, the digest of the index. A name without a registry host is a
// Docker Hub name (postgres:16 is docker.io/library/postgres:16); no tag
// means latest. Credentials and plain HTTP follow newRepository.
func ResolveImage(ctx context.Context, ref string) (string, error) {
	repo, tag := splitImage(ref)
	r, err := newRepository(repo)
	if err != nil {
		return "", err
	}
	d, err := r.Resolve(ctx, tag)
	if err != nil {
		return "", err
	}
	return d.Digest.String(), nil
}

// splitImage splits an image reference into its normalized repository and
// its digest, else its tag, else "latest".
func splitImage(ref string) (repo, tag string) {
	repo, tag, hasDigest := strings.Cut(ref, "@")
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		if !hasDigest {
			tag = repo[i+1:]
		}
		repo = repo[:i]
	}
	if tag == "" {
		tag = "latest"
	}
	host, _, found := strings.Cut(repo, "/")
	// The OCI distribution reference grammar: a name without a registry
	// host is on Docker Hub. This is the reference format's default, not a
	// vendor choice.
	switch {
	case !found:
		repo = "docker.io/library/" + repo
	case !strings.ContainsAny(host, ".:") && host != "localhost":
		repo = "docker.io/" + repo
	}
	return repo, tag
}
