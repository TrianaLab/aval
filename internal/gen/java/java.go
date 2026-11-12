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
// that is not a clean path inside the root, or that no contract glob can
// express (gen.Literal; metacharacters are escaped, a backslash is not).
func checkDir(dir string) error {
	if dir == "" || dir == "." || path.Clean(dir) != dir || path.IsAbs(dir) || dir == ".." || strings.HasPrefix(dir, "../") {
		return fmt.Errorf("%q: want a directory inside the root", dir)
	}
	_, err := gen.Literal(dir)
	return err
}

// under is gen.Under for a path checkDir accepted, or that extends one with
// a plain file name or pattern, which cannot fail.
func under(dir, pattern string) string {
	g, _ := gen.Under(dir, pattern)
	return g
}

// notContext are the top-level directories the root context leaves out:
// VCS and build tool state and build output.
var notContext = map[string]bool{".git": true, ".gradle": true, "build": true, "out": true, "target": true}

// rootContext returns the globs of every top-level entry of root that is
// not a unit directory (units) nor in notContext: entry/** for a
// directory, its name for a file. Build files there read root files
// (config/checkstyle.xml, ${main.basedir}, version catalogs) through too
// many APIs to follow one by one, so every unit gets them. A directory that
// holds units is included whole: more re-runs, never a false hit.
func rootContext(root string, units map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var globs []string
	for _, e := range entries {
		n := e.Name()
		if units[n] || notContext[n] {
			continue
		}
		g, err := gen.Literal(n)
		if e.IsDir() {
			g, err = gen.Under(n, "**")
		}
		if err != nil {
			return nil, err
		}
		globs = append(globs, g)
	}
	return globs, nil
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
