package acceptance

import (
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"
)

// The README "Try it on your laptop" path, run as written: every sh block
// between the quickstart markers, in order, in one shell. Two substitutions
// keep it offline: `docker` lines (registry:2 and its cleanup) are dropped
// and the README registry address points at an in-process registry. openssl
// must be on PATH, as the README says.
func TestReadmeQuickstart(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(readme), "<!-- quickstart:start")
	section, _, ok2 := strings.Cut(rest, "<!-- quickstart:end")
	if !ok || !ok2 {
		t.Fatal("README.md: quickstart markers not found")
	}
	var script, dropped []string
	for _, block := range regexp.MustCompile("(?s)```sh\n(.*?)```").FindAllStringSubmatch(section, -1) {
		for _, line := range strings.Split(block[1], "\n") {
			if strings.HasPrefix(line, "docker ") {
				dropped = append(dropped, line)
				continue
			}
			script = append(script, line)
		}
	}
	if len(dropped) == 0 || !strings.Contains(dropped[0], "registry:2") {
		t.Fatalf("README quickstart must start a local registry:2, dropped %q", dropped)
	}

	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	body := strings.ReplaceAll(strings.Join(script, "\n"), "localhost:5001", host)

	cmd := exec.Command("sh", "-e", "-c", body)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(aval)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DOCKER_CONFIG="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("quickstart failed: %v\n%s", err, out)
	}
	repo := host + "/demo/hello-aval"
	for _, want := range []string{
		"pushed " + repo + "@sha256:",
		"\nverified hello sha256:",
		"reused passed result " + repo + "@sha256:",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("quickstart output lacks %q\n%s", want, out)
		}
	}
}
