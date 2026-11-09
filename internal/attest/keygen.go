package attest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// WriteKeyPair writes a new ECDSA P-256 signing key to name.key
// (unencrypted PKCS#8 PEM, mode 0600) and its public key to name.pub (PKIX
// PEM, the file a policy's publicKey names), and returns the key id. It
// refuses to replace either file unless force is set; nothing is written
// when it refuses.
func WriteKeyPair(name string, force bool) (string, error) {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader) // cannot fail: crypto/rand never fails
	der, _ := x509.MarshalPKCS8PrivateKey(priv)                // cannot fail for a P-256 key
	pubDER, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)    // cannot fail for a P-256 key
	pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	files := []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{name + ".key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600},
		{name + ".pub", pub, 0o644},
	}
	// Check both paths before touching either, so a refusal writes and
	// removes nothing.
	for _, f := range files {
		st, err := os.Lstat(f.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return "", err
		case !force:
			return "", fmt.Errorf("%s already exists; pass --force to replace it", f.path)
		case st.IsDir():
			return "", fmt.Errorf("%s is a directory", f.path)
		}
	}
	if force {
		for _, f := range files {
			if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return "", err
			}
		}
	}
	// O_EXCL refuses a file or symlink planted after the check, so the key
	// never lands in someone else's file or keeps a looser mode.
	for i, f := range files {
		if err := writeNew(f.path, f.data, f.mode); err != nil {
			if i > 0 {
				_ = os.Remove(files[0].path) // no orphan name.key blocking a rerun
			}
			return "", err
		}
	}
	kid, _ := evidence.PublicKeyID(string(pub)) // cannot fail: a PKIX PEM just built
	return kid, nil
}

// openNew creates a file that must not exist yet; a variable so tests can
// plant a file between the check and the write.
var openNew = func(path string, mode os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
}

func writeNew(path string, data []byte, mode os.FileMode) error {
	fh, err := openNew(path, mode)
	if err != nil {
		return err
	}
	_, err = fh.Write(data)
	if cerr := fh.Close(); err == nil {
		err = cerr
	}
	return err
}
