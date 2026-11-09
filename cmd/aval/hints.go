package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/TrianaLab/aval/internal/cliout"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// listError is an error printed as its head on the aval: line and each member
// of err on an indented line below it (cliout.Problems).
type listError struct {
	head string
	err  error
}

func (l listError) Error() string { return l.head + " " + reason(l.err) }
func (l listError) Unwrap() error { return l.err }

// unreachable says what to do about a registry that cannot be dialled or
// did not answer in time: "cannot reach HOST (cause); ...". It is empty for
// any other error, and for a request to another host (a scope.resolve
// lookup, a Sigstore trusted root): the hint would blame the wrong one.
// Only messages use it: a report keeps the raw error.
// ponytail: a timeout without a *url.Error (mid-body) gets no hint; the raw error still prints.
func unreachable(repo string, err error) string {
	host, _, _ := strings.Cut(repo, "/")
	var ue *url.Error
	if host == "" || !errors.As(err, &ue) {
		return ""
	}
	if u, perr := url.Parse(ue.URL); perr != nil || u.Host != host {
		return ""
	}
	var dns *net.DNSError
	var op *net.OpError
	cause := ""
	switch {
	case errors.As(err, &dns):
		cause = dns.Err
	case errors.As(err, &op) && op.Op == "dial":
		cause = op.Err.Error()
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		cause = "timed out; see --registry-timeout"
	default:
		return ""
	}
	return fmt.Sprintf("cannot reach %s (%s); is the registry running? check %s or --registry", host, cause, registryEnv)
}

// reachError is err said as unreachable, when it is one; err otherwise.
// The exit code still follows err.
func reachError(repo string, err error) error {
	if msg := unreachable(repo, err); msg != "" {
		return hinted{msg, err}
	}
	return err
}

// hinted prints msg instead of err, and unwraps to err.
type hinted struct {
	msg string
	err error
}

func (h hinted) Error() string { return h.msg }
func (h hinted) Unwrap() error { return h.err }

// signingNeeds is the usage error for signing without a key, a token or a
// terminal: the three ways to sign.
func signingNeeds() error {
	return usageError{listError{"signing needs one of:", errors.Join(
		errors.New("--key FILE: a private key (create one with: aval keygen)"),
		fmt.Errorf("$%s: an OIDC token, for keyless signing", idTokenEnv),
		errors.New("--login, or a terminal: the interactive Sigstore login, for keyless signing"),
	)}}
}

// warnUntrustedKey warns when the policy at policyFile lists no publicKey
// for key: consumers with that policy will reject what it signs. Only a
// warning on w: it never changes the exit code or what is published. A
// policy or key that cannot be read says nothing here; the lookup and the
// signing report those.
func warnUntrustedKey(w io.Writer, key, policyFile string) {
	if key == "" || policyFile == "" {
		return
	}
	k, err := loadP256(key)
	if err != nil {
		return
	}
	pol, err := loadPolicy(policyFile)
	if err != nil {
		return
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey) // ponytail: cannot fail for a P-256 key
	kid, _ := evidence.PublicKeyID(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))) // ponytail: cannot fail for the PEM just encoded
	for _, id := range pol.Identities {
		if got, err := evidence.PublicKeyID(id.PublicKey); err == nil && got == kid {
			return
		}
	}
	fmt.Fprintf(w, "aval: warning: key %s (key id %s) is not in %s; consumers with this policy will reject the result\n", cliout.OneLine(key), kid, cliout.OneLine(policyFile))
}
