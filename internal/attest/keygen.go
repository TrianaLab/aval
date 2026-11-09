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
	for _, f := range files {
		if !force {
			if _, err := os.Lstat(f.path); !errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("%s already exists; pass --force to replace it", f.path)
			}
		} else if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	// ponytail: neither file exists now, so WriteFile creates both with
	// f.mode; a file planted between the check and the write is not guarded.
	for _, f := range files {
		if err := os.WriteFile(f.path, f.data, f.mode); err != nil {
			return "", err
		}
	}
	kid, _ := evidence.PublicKeyID(string(pub)) // cannot fail: a PKIX PEM just built
	return kid, nil
}
