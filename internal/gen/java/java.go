// Package java is the aval gen adapter for Java and Kotlin: one unit per
// Maven module or Gradle subproject, with an edge for each dependency on
// another module or subproject of the same build. It reads the build files
// (pom.xml, settings.gradle(.kts), build.gradle(.kts)) and runs no JVM tool,
// so its output does not depend on what is installed. Anything it cannot
// read with certainty is an error, never a guess (invariant 3).
package java

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/TrianaLab/aval/internal/gen"
)

// Adapter lists the units of the Maven or Gradle build at the contract root.
type Adapter struct{}

var _ gen.Adapter = Adapter{}

// Units reads the Maven build (pom.xml) or the Gradle build
// (settings.gradle.kts or settings.gradle) at root. The root project itself
// is not a unit: its build files are shared inputs for the defaults.
func (Adapter) Units(root string) ([]gen.Unit, error) {
	settings, err := oneOf(root, "settings.gradle.kts", "settings.gradle")
	if err != nil {
		return nil, err
	}
	pom := isFile(filepath.Join(root, "pom.xml"))
	switch {
	case pom && settings != "":
		return nil, fmt.Errorf("%s has both pom.xml and %s: keep one build", root, settings)
	case pom:
		return maven(root)
	case settings != "":
		return gradle(root, settings)
	}
	return nil, fmt.Errorf("%s has no pom.xml, settings.gradle.kts or settings.gradle", root)
}

// oneOf returns the one of names that is a file in dir, or "" for none.
func oneOf(dir string, names ...string) (string, error) {
	var found []string
	for _, n := range names {
		if isFile(filepath.Join(dir, filepath.FromSlash(n))) {
			found = append(found, n)
		}
	}
	if len(found) > 1 {
		return "", fmt.Errorf("%s has both %s: keep one", dir, strings.Join(found, " and "))
	}
	if len(found) == 0 {
		return "", nil
	}
	return found[0], nil
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// checkDir rejects a unit directory (slash-separated, relative to the root)
// that is not a clean path inside the root, or that holds a glob character
// the generated globs could not express.
func checkDir(dir string) error {
	if dir == "" || dir == "." || path.Clean(dir) != dir || path.IsAbs(dir) || dir == ".." || strings.HasPrefix(dir, "../") ||
		strings.ContainsAny(dir, `*?[]{}\`) {
		return fmt.Errorf("%q: want a directory inside the root, without glob characters", dir)
	}
	return nil
}

// source returns the globs of a unit's own files: its build file, when it
// has one, and src/**, when src is a directory. Sources outside src are
// rejected by the callers, so nothing the build compiles is left out.
func source(root, dir, build string) []string {
	var s []string
	if build != "" {
		s = append(s, dir+"/"+build)
	}
	if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), "src")); err == nil && info.IsDir() {
		s = append(s, dir+"/src/**")
	}
	return s
}

// tool returns the wrapper script at root (./mvnw, ./gradlew) when there is
// one, otherwise the tool on PATH.
func tool(root, wrapper, name string) string {
	if isFile(filepath.Join(root, wrapper)) {
		return "./" + wrapper
	}
	return name
}

// errUnsupported marks a build construct the adapter cannot map to files.
var errUnsupported = errors.New("not supported by aval gen java")
