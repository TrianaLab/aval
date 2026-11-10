// Package version reports the build version.
package version

// Version is set at build time with -ldflags.
var Version = "dev"

// String is the version for humans.
func String() string {
	return "v" + Version
}
