//go:build js

package resolve

import (
	"context"
	"errors"

	"github.com/TrianaLab/aval/internal/contract"
)

// All fails for any reference in the browser demo (cmd/aval-wasm): the
// resolvers' HTTP, zip and registry clients would push the module over its
// size budget. Fails closed, so the test runs.
func All(_ context.Context, refs []contract.Reference) ([]Result, error) {
	if len(refs) > 0 {
		return nil, errors.New("scope.resolve is not available in the browser")
	}
	return nil, nil
}
