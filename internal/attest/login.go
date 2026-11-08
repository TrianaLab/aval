package attest

import (
	"io"

	"github.com/sigstore/sigstore/pkg/oauthflow"
)

// The public Sigstore login: the OIDC issuer in front of Fulcio for people
// (GitHub, Google or Microsoft accounts) and its public client id.
const (
	DefaultOIDCIssuer   = "https://oauth2.sigstore.dev/auth"
	DefaultOIDCClientID = "sigstore"
)

// Login gets an OIDC token for keyless signing through the interactive
// browser login (authorization code with PKCE) at issuer. open opens the
// login URL; nil means the default browser. If it fails, the login URL is
// printed to out and the code is read from in. The token is returned, never
// stored.
func Login(issuer, clientID string, in io.Reader, out io.Writer, open func(string) error) (string, error) {
	tg := *oauthflow.DefaultIDTokenGetter
	tg.Input, tg.Output, tg.BrowserOpener = in, out, open
	tok, err := oauthflow.OIDConnect(issuer, clientID, "", "", &tg)
	if err != nil {
		return "", err
	}
	return tok.RawString, nil
}
