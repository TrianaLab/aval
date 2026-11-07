package resolve

import "time"

// Result is one resolved reference.
type Result struct {
	Kind, Ref string
	// Version is what the name resolved to (a version, a ref name),
	// "floating" for a spec that names no exact version, "-" when there is
	// none; ID is the immutable id hashed into the fingerprint.
	Version, ID string
	// Took is the time the resolution took, reported by --explain.
	Took time.Duration
}
