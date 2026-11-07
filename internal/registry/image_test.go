package registry

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestSplitImage(t *testing.T) {
	for ref, want := range map[string]string{
		"postgres":                         "docker.io/library/postgres latest",
		"postgres:16":                      "docker.io/library/postgres 16",
		"bitnami/redis:7":                  "docker.io/bitnami/redis 7",
		"docker.io/postgres:16":            "docker.io/library/postgres 16",
		"index.docker.io/postgres":         "docker.io/library/postgres latest",
		"docker.io/library/postgres:16":    "docker.io/library/postgres 16",
		"index.docker.io/bitnami/redis:7":  "docker.io/bitnami/redis 7",
		"ghcr.io/acme/db:1":                "ghcr.io/acme/db 1",
		"localhost/db":                     "localhost/db latest",
		"127.0.0.1:5000/acme/db":           "127.0.0.1:5000/acme/db latest",
		"127.0.0.1:5000/acme/db:2":         "127.0.0.1:5000/acme/db 2",
		"redis@sha256:abc":                 "docker.io/library/redis sha256:abc",
		"ghcr.io/acme/db:1@sha256:abc":     "ghcr.io/acme/db sha256:abc",
		"127.0.0.1:5000/acme/db@sha256:ab": "127.0.0.1:5000/acme/db sha256:ab",
	} {
		repo, tag := splitImage(ref)
		if repo+" "+tag != want {
			t.Errorf("splitImage(%s) = %s %s, want %s", ref, repo, tag, want)
		}
	}
}

func TestResolveImage(t *testing.T) {
	repo := serve(t, false, nil)
	host, _, _ := strings.Cut(repo, "/")
	m := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[]}`
	req, _ := http.NewRequest(http.MethodPut, "http://"+host+"/v2/acme/db/manifests/16", strings.NewReader(m))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	d, err := ResolveImage(context.Background(), host+"/acme/db:16")
	if err != nil || !strings.HasPrefix(d, "sha256:") {
		t.Fatalf("ResolveImage = %s, %v", d, err)
	}
	if _, err := ResolveImage(context.Background(), host+"/acme/db:17"); err == nil {
		t.Fatal("a missing tag must fail")
	}
	if _, err := ResolveImage(context.Background(), "Bad Name"); err == nil {
		t.Fatal("an invalid reference must fail")
	}
}
