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
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/attest"
)

// Seams for tests. The latest tag comes from the redirect of
// /releases/latest, not api.github.com, which rate-limits anonymous calls
// (same reason as scripts/get-aval.sh).
var (
	releasesURL  = "https://github.com/TrianaLab/aval/releases"
	httpClient   = &http.Client{Timeout: 5 * time.Minute}
	maxDownload  = int64(256 << 20)
	executable   = os.Executable
	verifyBlob   = attest.VerifyBlob
	goos, goarch = runtime.GOOS, runtime.GOARCH
)

// The identity the release workflow signs checksums.txt with; the tag is
// appended (scripts/get-aval.sh pins the same one).
const (
	releaseIssuer = "https://token.actions.githubusercontent.com"
	releaseSigner = "https://github.com/TrianaLab/aval/.github/workflows/release.yaml@refs/tags/"
)

var releaseTag = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the aval version, Go version and platform",
		Args:  noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			fmt.Fprintf(c.OutOrStdout(), "aval %s (%s %s/%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}

func newUpgradeCmd() *cobra.Command {
	var want string
	var check bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Replace this binary with a verified aval release",
		Long: "upgrade downloads a release from GitHub Releases (the latest, or --version), checks that\n" +
			"checksums.txt carries a keyless Sigstore signature from Aval's release workflow at that\n" +
			"tag and that the archive matches it, then replaces the running binary. Any failed\n" +
			"check replaces nothing.",
		Args: noArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if want != "" && !releaseTag.MatchString(want) {
				return usageError{fmt.Errorf("--version %q is not a release version such as v1.2.3", want)}
			}
			return upgrade(c.OutOrStdout(), want, check)
		},
	}
	cmd.Flags().StringVar(&want, "version", "", "release to install, e.g. v0.4.0 (default: latest)")
	cmd.Flags().BoolVar(&check, "check", false, "print the current and target versions without installing")
	return cmd
}

func upgrade(out io.Writer, want string, check bool) error {
	tag := "v" + strings.TrimPrefix(want, "v")
	if want == "" {
		var err error
		if tag, err = latestTag(); err != nil {
			return fmt.Errorf("resolving the latest release: %w", err)
		}
	}
	cur, next := strings.TrimPrefix(version, "v"), strings.TrimPrefix(tag, "v")
	switch {
	case cur == next:
		fmt.Fprintf(out, "aval %s is up to date\n", cur)
		return nil
	case check:
		fmt.Fprintf(out, "current %s\nlatest  %s\n", cur, next)
		return nil
	}
	base := releasesURL + "/download/" + tag + "/"
	sums, err := download(base + "checksums.txt")
	if err != nil {
		return err
	}
	bundle, err := download(base + "checksums.txt.sigstore.json")
	if err != nil {
		return err
	}
	// Who published checksums.txt, before any sum in it is trusted.
	if err := verifyBlob("", bundle, sums, releaseIssuer, releaseSigner+tag); err != nil {
		return fmt.Errorf("checksums.txt is not signed by %s%s: %w", releaseSigner, tag, err)
	}
	archive, name := fmt.Sprintf("aval_%s_%s_%s.tar.gz", next, goos, goarch), "aval"
	if goos == "windows" {
		archive, name = strings.TrimSuffix(archive, ".tar.gz")+".zip", "aval.exe"
	}
	sum, ok := sumFor(sums, archive)
	if !ok {
		return fmt.Errorf("checksums.txt does not list %s", archive)
	}
	data, err := download(base + archive)
	if err != nil {
		return err
	}
	if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != sum {
		return fmt.Errorf("checksum mismatch for %s", archive)
	}
	bin, err := extract(data, name)
	if err != nil {
		return fmt.Errorf("%s: %w", archive, err)
	}
	if err := replace(bin); err != nil {
		return err
	}
	fmt.Fprintf(out, "upgraded aval %s -> %s\n", cur, next)
	return nil
}

func latestTag() (string, error) {
	c := *httpClient
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(releasesURL + "/latest")
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	loc, err := resp.Location()
	if err != nil {
		return "", err
	}
	// The tag goes into URLs and the signer identity.
	if tag := path.Base(loc.Path); releaseTag.MatchString(tag) {
		return tag, nil
	}
	return "", fmt.Errorf("redirect to %s does not name a release version", loc)
}

// download GETs url, refusing anything but 200 and bodies over maxDownload.
func download(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err == nil && int64(len(b)) > maxDownload {
		err = fmt.Errorf("GET %s: larger than %d bytes", url, maxDownload)
	}
	return b, err
}

// sumFor returns the sha256 that checksums.txt lists for exactly name: a
// substring match would let name.sbom.json answer for the archive.
func sumFor(sums []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name {
			return f[0], true
		}
	}
	return "", false
}

// extract returns the archive entry called name. ponytail: no size cap on
// the entry, the archive's sha256 is already pinned by the signed checksums.
func extract(archive []byte, name string) ([]byte, error) {
	if goos == "windows" {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if f.Name == name {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer func() { _ = rc.Close() }()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("no %s entry", name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("no %s entry: %w", name, err)
		}
		if h.Name == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
}

// replace writes bin next to the running executable and renames it over it,
// so a failure leaves the old binary in place. Windows cannot replace a
// running exe, only rename it, so the old one moves to <exe>.old first.
func replace(bin []byte) error {
	exe, err := executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return fmt.Errorf("locating the aval binary: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".aval-upgrade-*")
	if err == nil {
		defer func() { _ = os.Remove(tmp.Name()) }() // a no-op once renamed
		_, err = tmp.Write(bin)
		err = errors.Join(err, tmp.Chmod(0o755), tmp.Close())
	}
	if err == nil && goos == "windows" {
		err = os.Rename(exe, exe+".old")
	}
	if err == nil {
		err = os.Rename(tmp.Name(), exe)
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("replacing %s: %w (run with sudo, or reinstall with the installer)", exe, err)
	}
	if err != nil {
		return fmt.Errorf("replacing %s: %w", exe, err)
	}
	return nil
}
