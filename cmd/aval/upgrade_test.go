package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	code, out, _ := runCLI("version")
	want := fmt.Sprintf("aval %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if code != ExitOK || out != want {
		t.Fatalf("got %d %q, want %q", code, out, want)
	}
	if code, _, _ := runCLI("version", "x"); code != ExitUsage {
		t.Fatalf("stray arg: exit %d", code)
	}
}

func tgz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("MI"))
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	return b.Bytes()
}

func zipped(t *testing.T, name string, body []byte, method uint16) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, err := zw.CreateRaw(&zip.FileHeader{Name: name, Method: method, CompressedSize64: uint64(len(body)), UncompressedSize64: uint64(len(body))})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(body)
	_ = zw.Close()
	return b.Bytes()
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

const newBin = "new aval binary"

// release serves a fake GitHub release v9.9.9 for linux/amd64 and makes the
// running binary a temp file holding "old". The fake signature check accepts
// a bundle that names the signer identity it is checked against.
type release struct {
	files map[string][]byte // path under /download/v9.9.9/
	exe   string
	srv   *httptest.Server
}

func newRelease(t *testing.T) *release {
	t.Helper()
	r := &release{files: map[string][]byte{}}
	archive := tgz(t, "aval", []byte(newBin))
	r.files["aval_9.9.9_linux_amd64.tar.gz"] = archive
	r.files["checksums.txt"] = []byte(hexSum(archive) + "  aval_9.9.9_linux_amd64.tar.gz.sbom.json\n" + hexSum(archive) + "  aval_9.9.9_linux_amd64.tar.gz\n")
	r.files["checksums.txt.sigstore.json"] = []byte(releaseSigner + "v9.9.9")
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/TrianaLab/aval/releases/tag/v9.9.9", http.StatusFound)
	})
	mux.HandleFunc("/nov/latest", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/TrianaLab/aval/releases/tag/9.9.9", http.StatusFound)
	})
	mux.HandleFunc("/bad/latest", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/TrianaLab/aval/releases/tag/main", http.StatusFound)
	})
	mux.HandleFunc("/down/latest", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusGatewayTimeout) })
	mux.HandleFunc("/noloc/latest", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusFound) })
	mux.HandleFunc("/download/", func(w http.ResponseWriter, req *http.Request) {
		b, ok := r.files[strings.TrimPrefix(req.URL.Path, "/download/v9.9.9/")]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(b)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	r.exe = filepath.Join(t.TempDir(), "aval")
	if err := os.WriteFile(r.exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldURL, oldExe, oldVerify, oldOS, oldArch, oldVersion, oldMax := releasesURL, executable, verifyBlob, goos, goarch, version, maxDownload
	t.Cleanup(func() {
		releasesURL, executable, verifyBlob, goos, goarch, version, maxDownload = oldURL, oldExe, oldVerify, oldOS, oldArch, oldVersion, oldMax
	})
	releasesURL, goos, goarch, version = r.srv.URL, "linux", "amd64", "1.0.0"
	executable = func() (string, error) { return r.exe, nil }
	verifyBlob = func(trustedRoot string, bundle, _ []byte, issuer, subject string) error {
		if trustedRoot != "" || issuer != releaseIssuer || string(bundle) != subject {
			return errors.New("no matching certificate identity")
		}
		return nil
	}
	return r
}

func (r *release) binary(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(r.exe)
	return string(b)
}

func TestUpgrade(t *testing.T) {
	r := newRelease(t)
	code, out, errOut := runCLI("upgrade")
	if code != ExitOK || out != "upgraded aval 1.0.0 -> 9.9.9\n" || r.binary(t) != newBin {
		t.Fatalf("exit %d, out %q, err %q, binary %q", code, out, errOut, r.binary(t))
	}
	if fi, _ := os.Stat(r.exe); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(r.exe), ".aval-upgrade-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestUpgradeThroughSymlink(t *testing.T) {
	r := newRelease(t)
	link := filepath.Join(t.TempDir(), "aval")
	_ = os.Symlink(r.exe, link)
	executable = func() (string, error) { return link, nil }
	if code, _, errOut := runCLI("upgrade", "--version", "9.9.9"); code != ExitOK || r.binary(t) != newBin {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestUpgradeWindows(t *testing.T) {
	r := newRelease(t)
	goos = "windows"
	archive := zipped(t, "aval.exe", []byte(newBin), zip.Store)
	r.files["aval_9.9.9_windows_amd64.zip"] = archive
	r.files["checksums.txt"] = []byte(hexSum(archive) + "  aval_9.9.9_windows_amd64.zip\n")
	if code, _, errOut := runCLI("upgrade"); code != ExitOK || r.binary(t) != newBin {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if old, _ := os.ReadFile(r.exe + ".old"); string(old) != "old" {
		t.Fatalf("old exe not kept: %q", old)
	}
}

// A redirect to a tag without the v still builds v9.9.9 URLs and identity.
func TestUpgradeLatestWithoutV(t *testing.T) {
	r := newRelease(t)
	releasesURL += "/nov"
	r.srv.Config.Handler = http.StripPrefix("/nov", r.srv.Config.Handler)
	if code, _, errOut := runCLI("upgrade"); code != ExitOK || r.binary(t) != newBin {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

// An older latest is refused; an explicit --version downgrades.
func TestUpgradeDowngrade(t *testing.T) {
	r := newRelease(t)
	version = "10.0.0"
	if code, _, errOut := runCLI("upgrade"); code != ExitError || !strings.Contains(errOut, "pass --version v9.9.9 to downgrade") || r.binary(t) != "old" {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	code, out, errOut := runCLI("upgrade", "--version", "v9.9.9")
	if code != ExitOK || out != "downgraded aval 10.0.0 -> 9.9.9\n" || r.binary(t) != newBin {
		t.Fatalf("exit %d, out %q: %s", code, out, errOut)
	}
}

// A dev build is always replaced.
func TestUpgradeDevBuild(t *testing.T) {
	r := newRelease(t)
	version = "dev"
	if code, out, errOut := runCLI("upgrade"); code != ExitOK || out != "upgraded aval dev -> 9.9.9\n" || r.binary(t) != newBin {
		t.Fatalf("exit %d, out %q: %s", code, out, errOut)
	}
}

// If the new exe cannot take the old one's place, the old one moves back.
func TestUpgradeWindowsRollback(t *testing.T) {
	r := newRelease(t)
	goos = "windows"
	r.signAs("aval_9.9.9_windows_amd64.zip", zipped(t, "aval.exe", []byte(newBin), zip.Store))
	t.Cleanup(func() { rename = os.Rename })
	rename = func(from, to string) error {
		if strings.Contains(from, ".aval-upgrade-") {
			return errors.New("sharing violation")
		}
		return os.Rename(from, to)
	}
	if code, _, errOut := runCLI("upgrade"); code != ExitError || !strings.Contains(errOut, "sharing violation") || r.binary(t) != "old" {
		t.Fatalf("exit %d: %s, binary %q", code, errOut, r.binary(t))
	}
}

func TestUpgradeNoChange(t *testing.T) {
	for name, tc := range map[string]struct {
		version string
		args    []string
		want    string
	}{
		"up to date":           {"9.9.9", nil, "aval 9.9.9 is up to date\n"},
		"up to date, v prefix": {"v9.9.9", []string{"--version", "v9.9.9"}, "aval 9.9.9 is up to date\n"},
		"check":                {"1.0.0", []string{"--check"}, "current 1.0.0\ntarget  9.9.9\n"},
		"check older":          {"1.0.0", []string{"--check", "--version", "v0.3.0-rc.1"}, "current 1.0.0\ntarget  0.3.0-rc.1\n"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRelease(t)
			version = tc.version
			r.files = nil // nothing may be downloaded
			code, out, errOut := runCLI(append([]string{"upgrade"}, tc.args...)...)
			if code != ExitOK || out != tc.want || r.binary(t) != "old" {
				t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
			}
		})
	}
}

// Every failed check exits 1 (2 for a bad flag) and leaves the binary alone.
func TestUpgradeFailsClosed(t *testing.T) {
	cases := map[string]struct {
		setup func(*testing.T, *release)
		args  []string
		code  int
		want  string
	}{
		"bad --version":    {nil, []string{"--version", "latest"}, ExitUsage, "not a release version"},
		"stray arg":        {nil, []string{"x"}, ExitUsage, "unknown command"},
		"server down":      {func(_ *testing.T, r *release) { r.srv.Close() }, nil, ExitError, "resolving the latest release"},
		"no redirect":      {func(_ *testing.T, r *release) { releasesURL += "/download/v9.9.9/checksums.txt#" }, nil, ExitError, "200 OK, not a redirect"},
		"latest 504":       {func(_ *testing.T, r *release) { releasesURL += "/down" }, nil, ExitError, "504 Gateway Timeout, not a redirect"},
		"no Location":      {func(_ *testing.T, r *release) { releasesURL += "/noloc" }, nil, ExitError, "no Location"},
		"redirect not tag": {func(_ *testing.T, r *release) { releasesURL += "/bad" }, nil, ExitError, "does not name a release version"},
		"release missing":  {nil, []string{"--version", "v9.9.8"}, ExitError, "404"},
		"download fails": {func(_ *testing.T, r *release) {
			old := r.srv.Config.Handler
			r.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "checksums.txt") {
					w.Header().Set("Content-Length", "100")
					_, _ = w.Write([]byte("short"))
					return
				}
				old.ServeHTTP(w, req)
			})
		}, nil, ExitError, "unexpected EOF"},
		"no connection": {func(_ *testing.T, r *release) {
			old := r.srv.Config.Handler
			r.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "checksums.txt") {
					c, _, _ := w.(http.Hijacker).Hijack()
					_ = c.Close()
					return
				}
				old.ServeHTTP(w, req)
			})
		}, nil, ExitError, "EOF"},
		"no bundle": {func(_ *testing.T, r *release) { delete(r.files, "checksums.txt.sigstore.json") }, nil, ExitError, "404"},
		"wrong identity": {func(_ *testing.T, r *release) {
			r.files["checksums.txt.sigstore.json"] = []byte(releaseSigner + "v1.0.0")
		}, nil, ExitError, "is not signed by " + releaseSigner + "v9.9.9"},
		"over cap": {func(_ *testing.T, _ *release) { maxDownload = 10 }, nil, ExitError, "larger than 10 bytes"},
		"not listed": {func(_ *testing.T, r *release) {
			r.files["checksums.txt"] = []byte("abc  aval_9.9.9_linux_amd64.tar.gz.sbom.json\n")
		}, nil, ExitError, "does not list aval_9.9.9_linux_amd64.tar.gz"},
		"archive missing": {func(_ *testing.T, r *release) { delete(r.files, "aval_9.9.9_linux_amd64.tar.gz") }, nil, ExitError, "404"},
		"tampered archive": {func(t *testing.T, r *release) {
			r.files["aval_9.9.9_linux_amd64.tar.gz"] = tgz(t, "aval", []byte("evil"))
		}, nil, ExitError, "checksum mismatch"},
		"no aval entry": {func(t *testing.T, r *release) { r.sign(tgz(t, "bin/aval", []byte(newBin))) }, nil, ExitError, "no aval entry: EOF"},
		"not gzip":      {func(_ *testing.T, r *release) { r.sign([]byte("plain")) }, nil, ExitError, "tar.gz: unexpected EOF"},
		"zip no entry": {func(t *testing.T, r *release) {
			goos = "windows"
			r.signAs("aval_9.9.9_windows_amd64.zip", zipped(t, "aval", []byte(newBin), zip.Store))
		}, nil, ExitError, "no aval.exe entry"},
		"not zip": {func(t *testing.T, r *release) {
			goos = "windows"
			r.signAs("aval_9.9.9_windows_amd64.zip", []byte("plain"))
		}, nil, ExitError, "zip"},
		"zip bad method": {func(t *testing.T, r *release) {
			goos = "windows"
			r.signAs("aval_9.9.9_windows_amd64.zip", zipped(t, "aval.exe", []byte(newBin), 99))
		}, nil, ExitError, "unsupported compression"},
		"no executable": {func(_ *testing.T, _ *release) {
			executable = func() (string, error) { return "", errors.New("no /proc") }
		}, nil, ExitError, "locating the aval binary: no /proc"},
		"dangling executable": {func(_ *testing.T, r *release) { _ = os.Remove(r.exe) }, nil, ExitError, "locating the aval binary"},
	}
	if os.Geteuid() != 0 {
		cases["read-only dir"] = struct {
			setup func(*testing.T, *release)
			args  []string
			code  int
			want  string
		}{func(t *testing.T, r *release) {
			dir := filepath.Dir(r.exe)
			_ = os.Chmod(dir, 0o555)
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		}, nil, ExitError, "run with sudo, or reinstall with the installer"}
		cases["windows rename fails"] = struct {
			setup func(*testing.T, *release)
			args  []string
			code  int
			want  string
		}{func(t *testing.T, r *release) {
			goos = "windows"
			r.signAs("aval_9.9.9_windows_amd64.zip", zipped(t, "aval.exe", []byte(newBin), zip.Store))
			_ = os.Mkdir(r.exe+".old", 0o755) // a non-empty directory cannot be renamed over
			_ = os.WriteFile(filepath.Join(r.exe+".old", "x"), nil, 0o644)
		}, nil, ExitError, "aval.old"}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRelease(t)
			if tc.setup != nil {
				tc.setup(t, r)
			}
			code, _, errOut := runCLI(append([]string{"upgrade"}, tc.args...)...)
			if code != tc.code || !strings.Contains(errOut, tc.want) {
				t.Fatalf("exit %d, stderr %q, want %d and %q", code, errOut, tc.code, tc.want)
			}
			if b, err := os.ReadFile(r.exe); err == nil && string(b) != "old" {
				t.Fatalf("binary replaced: %q", b)
			}
		})
	}
}

func (r *release) sign(archive []byte) { r.signAs("aval_9.9.9_linux_amd64.tar.gz", archive) }

func (r *release) signAs(name string, archive []byte) {
	r.files[name] = archive
	r.files["checksums.txt"] = []byte(hexSum(archive) + "  " + name + "\n")
}
