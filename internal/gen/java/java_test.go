package java

import (
	"encoding/xml"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/gen"
)

// tree writes files under a new temporary directory and returns it.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// generate runs the adapter on root and rewrites set with its units.
func generate(t *testing.T, root string, set []byte) []byte {
	t.Helper()
	units, err := Adapter{}.Units(root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := gen.Rewrite(set, "java", units)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The fixtures under testdata generate their golden aval.golden.yaml byte
// for byte, on every run and platform.
func TestGolden(t *testing.T) {
	for _, build := range []string{"maven", "gradle"} {
		root := filepath.Join("testdata", build)
		want := read(t, filepath.Join(root, "aval.golden.yaml"))
		got := generate(t, root, read(t, filepath.Join(root, "aval.yaml")))
		if string(got) != string(want) {
			t.Errorf("%s: generated file differs from the golden:\n%s", build, gen.Diff("golden", "generated", want, got))
		}
		if again := generate(t, root, got); string(again) != string(got) {
			t.Errorf("%s: a second run changed the file", build)
		}
	}
}

// fixture loads a testdata build into memory.
func fixture(t *testing.T, build string) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	err := fs.WalkDir(os.DirFS(filepath.Join("testdata", build)), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			fsys[p] = &fstest.MapFile{Data: read(t, filepath.Join("testdata", build, p))}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return fsys
}

// digest fingerprints the generated test name of the golden file over fsys.
func digest(t *testing.T, fsys fstest.MapFS, name string) string {
	t.Helper()
	cs, err := contract.ParseAll(fsys["aval.golden.yaml"].Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Metadata.Name == name {
			fp, err := fingerprint.ComputeFS(fsys, c, fingerprint.Env{})
			if err != nil {
				t.Fatal(err)
			}
			return fp.Digest
		}
	}
	t.Fatalf("no test %s", name)
	return ""
}

func TestFalseHitTransitiveDependency(t *testing.T) {
	for _, tc := range []struct {
		build, test, transitive, shared, parent, unrelatedTest, unrelated string
	}{
		// web -> api -> inventory -> common; tools stands alone.
		{"maven", "java.services.web", "common/src/main/java/com/acme/common/Money.java", "pom.xml", "services/pom.xml",
			"java.services.web", "tools/src/main/java/com/acme/tools/Gen.java"},
		// web -> api -> inventory -> common; services/build.gradle.kts configures web.
		// gradlew is in every unit's root context; common does not use inventory.
		{"gradle", "java.services.web", "common/src/main/kotlin/com/acme/common/Money.kt", "gradlew", "services/build.gradle.kts",
			"java.common", "inventory/src/main/java/com/acme/inventory/Stock.java"},
	} {
		fsys := fixture(t, tc.build)
		for _, c := range []struct {
			test, file string
			reuse      bool
		}{{tc.test, tc.transitive, false}, {tc.test, tc.shared, false}, {tc.test, tc.parent, false}, {tc.unrelatedTest, tc.unrelated, true}} {
			changed := maps.Clone(fsys)
			changed[c.file] = &fstest.MapFile{Data: append(read(t, filepath.Join("testdata", tc.build, c.file)), "// changed\n"...)}
			if got := digest(t, changed, c.test) == digest(t, fsys, c.test); got != c.reuse {
				t.Errorf("%s: %s after changing %s: same fingerprint %v, want %v", tc.build, c.test, c.file, got, c.reuse)
			}
		}
	}
}

// A new reference to a local module is drift: --check sees the file change
// and the diff names the fragment the test now uses.
func TestFalseHitNewReferenceIsDrift(t *testing.T) {
	for _, tc := range []struct{ build, file, old, new, want string }{
		{"maven", "tools/pom.xml", "  <artifactId>tools</artifactId>\n",
			"  <artifactId>tools</artifactId>\n  <dependencies>\n    <dependency>\n      <groupId>com.acme</groupId>\n      <artifactId>common</artifactId>\n    </dependency>\n  </dependencies>\n",
			"+      - java.common\n"},
		{"gradle", "common/build.gradle.kts", "}\n", "}\ndependencies {\n    testImplementation(project(\":services:api\"))\n}\n",
			"+      - java.services.api\n"},
	} {
		root := t.TempDir()
		if err := os.CopyFS(root, os.DirFS(filepath.Join("testdata", tc.build))); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(root, filepath.FromSlash(tc.file))
		if err := os.WriteFile(p, []byte(strings.Replace(string(read(t, p)), tc.old, tc.new, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		golden := read(t, filepath.Join(root, "aval.golden.yaml"))
		got := generate(t, root, golden)
		if d := gen.Diff("aval.yaml", "generated", golden, got); !strings.Contains(d, tc.want) {
			t.Errorf("%s: the diff does not show the new reference:\n%s", tc.build, d)
		}
	}
}

const pomHead = `<project><groupId>g</groupId><artifactId>root</artifactId>`

// inherited is a reactor whose root POM names common in a plugin's
// dependencies (a shared build-tools module) and lint in pluginManagement,
// resolves ${lib} per module (app sets it to util), and app imports bom as
// a BOM.
var inherited = map[string]string{
	"pom.xml": pomHead + `<properties><lib>common</lib></properties>
		<modules><module>common</module><module>util</module><module>lint</module><module>tools</module><module>bom</module><module>app</module></modules>
		<dependencies><dependency><groupId>g</groupId><artifactId>${lib}</artifactId></dependency></dependencies>
		<build><plugins><plugin><artifactId>maven-checkstyle-plugin</artifactId><dependencies><dependency><groupId>g</groupId><artifactId>common</artifactId></dependency></dependencies></plugin></plugins>
		<pluginManagement><plugins><plugin><groupId>g</groupId><artifactId>lint</artifactId></plugin></plugins></pluginManagement></build></project>`,
	"common/pom.xml":       `<project><groupId>g</groupId><artifactId>common</artifactId></project>`,
	"common/src/C.java":    "",
	"common/lombok.config": "",
	"util/pom.xml":         `<project><groupId>g</groupId><artifactId>util</artifactId></project>`,
	"lint/pom.xml":         `<project><groupId>g</groupId><artifactId>lint</artifactId></project>`,
	"tools/pom.xml":        `<project><groupId>g</groupId><artifactId>tools</artifactId></project>`,
	"bom/pom.xml":          `<project><groupId>g</groupId><artifactId>bom</artifactId></project>`,
	"app/pom.xml": `<project><groupId>g</groupId><artifactId>app</artifactId><properties><lib>util</lib></properties><dependencyManagement><dependencies>
		<dependency><groupId>g</groupId><artifactId>bom</artifactId><type>pom</type><scope>import</scope></dependency></dependencies></dependencyManagement></project>`,
}

// conventions is a Gradle build whose convention plugin in buildSrc names b,
// and whose c depends on a task of d.
var conventions = map[string]string{
	"settings.gradle.kts":                        `rootProject.name = "include-demo"` + "\n" + `include(":a", ":b", ":c", ":d", ":e")` + "\n" + `gradle.beforeProject { extra["shared"] = project(":e") }`,
	"build.gradle.kts":                           `subprojects { checkstyle { configFile = rootProject.file("config/checkstyle.xml") } }`,
	"config/checkstyle.xml":                      "",
	"config/detekt.yml":                          "",
	"buildSrc/src/main/kotlin/detekt.gradle.kts": `detekt { config.setFrom(rootProject.file("config/detekt.yml")) }`,
	"e/src/E.kt":                                 "",
	"buildSrc/src/main/kotlin/conv.gradle.kts":   `dependencies { "implementation"(project(":b")) }`,
	"buildSrc/build/generated/Ignored.kt":        "project(name)",
	"a/src/A.kt":                                 "",
	"b/src/B.kt":                                 "",
	"c/build.gradle.kts":                         `tasks.test { dependsOn(":d:jar") }`,
	"d/build.gradle.kts":                         `evaluationDependsOn(":b")`,
	"d/libs/x.jar":                               "",
	"buildSrc/README.md":                         "project(name) is not code here",
}

// nested is a Gradle build whose project app hands a file of its own to
// its subproject app:sub.
var nested = map[string]string{
	"settings.gradle.kts":       `include(":app", ":app:sub")`,
	"app/build.gradle.kts":      `subprojects { checkstyle { configFile = file("config/checkstyle.xml") } }`,
	"app/gradle.properties":     "",
	"app/config/checkstyle.xml": "",
	"app/sub/src/S.kt":          "",
}

// rooted is a Maven build whose module a reads a root file by a path
// relative to the root (the working directory under mvn -pl).
var rooted = map[string]string{
	"pom.xml":               pomHead + `<modules><module>a</module><module>b</module></modules></project>`,
	"a/pom.xml":             `<project><groupId>g</groupId><artifactId>a</artifactId><build><plugins><plugin><configuration><configLocation>config/checkstyle.xml</configLocation></configuration></plugin></plugins></build></project>`,
	"b/pom.xml":             `<project><groupId>g</groupId><artifactId>b</artifactId></project>`,
	"config/checkstyle.xml": "",
	".mvn/jvm.config":       "-Xmx1g",
}

// The files and edges a unit gets from inheritance, build logic and its own
// directory: a change to any of them must change its fingerprint.
func TestFalseHitInheritedAndImported(t *testing.T) {
	for _, tc := range []struct {
		files   map[string]string
		changes map[string]string // test -> file
	}{
		{inherited, map[string]string{
			"java.tools": "common/src/C.java", // root plugin dependency
			"java.bom":   "lint/pom.xml",      // root pluginManagement
			"java.app":   "bom/pom.xml",       // imported BOM
			"java.util":  "common/lombok.config",
		}},
		{inherited, map[string]string{"java.app": "util/pom.xml", "java.common": "common/lombok.config"}}, // ${lib} resolved in app's context
		{conventions, map[string]string{
			"java.a": "buildSrc/src/main/kotlin/conv.gradle.kts",
			"java.c": "b/src/B.kt", // c -> d (task path) -> b (evaluationDependsOn)
			"java.d": "d/libs/x.jar",
			"java.b": "config/detekt.yml",     // read by a buildSrc convention plugin
			"java.e": "config/checkstyle.xml", // read by the root build script
		}},
		{conventions, map[string]string{"java.a": "e/src/E.kt"}},                 // named in the settings script
		{nested, map[string]string{"java.app.sub": "app/gradle.properties"}},     // inherited by subprojects
		{nested, map[string]string{"java.app.sub": "app/config/checkstyle.xml"}}, // handed down by app's script
		{rooted, map[string]string{
			"java.a": "config/checkstyle.xml", // <configLocation> resolved against the root
			"java.b": ".mvn/jvm.config",
		}},
	} {
		set := generate(t, tree(t, tc.files), []byte("apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\n"))
		fsys := fstest.MapFS{"aval.golden.yaml": {Data: set}}
		for k, v := range tc.files {
			fsys[k] = &fstest.MapFile{Data: []byte(v)}
		}
		for test, file := range tc.changes {
			before := digest(t, fsys, test)
			changed := maps.Clone(fsys)
			changed[file] = &fstest.MapFile{Data: []byte("<!-- changed -->")}
			if digest(t, changed, test) == before {
				t.Errorf("%s: same fingerprint after changing %s", test, file)
			}
		}
	}
}

func TestUnits(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  []gen.Unit
	}{
		"maven wrapper, plugin, property from a parent, module listed twice": {
			files: map[string]string{
				"mvnw":    "",
				"pom.xml": pomHead + `<properties><plug>plugin</plug></properties><modules><module>a</module><module>b/</module></modules><profiles><profile><modules><module>a</module></modules></profile></profiles></project>`,
				"a/pom.xml": `<project><parent><groupId>g</groupId><artifactId>root</artifactId></parent><artifactId>a</artifactId>
					<build><sourceDirectory>${project.basedir}/src/main/java</sourceDirectory><plugins><plugin><groupId>g</groupId><artifactId>${plug}</artifactId></plugin><plugin><artifactId>maven-x-plugin</artifactId><dependencies><dependency><groupId>${project.parent.groupId}</groupId><artifactId>b</artifactId></dependency></dependencies></plugin></plugins></build></project>`,
				"a/src/A.java":   "",
				"b/pom.xml":      `<project><groupId>g</groupId><artifactId>b</artifactId><dependencies><dependency><groupId>${pom.groupId}</groupId><artifactId>${artifactId}</artifactId></dependency></dependencies></project>`,
				"plugin/pom.xml": "not listed as a module",
			},
			want: []gen.Unit{
				{ID: "a", Source: []string{"mvnw", "plugin/**", "pom.xml", "a/**"}, Deps: []string{"b"}, Command: []string{"./mvnw", "-B", "-pl", "a", "test"}},
				{ID: "b", Source: []string{"mvnw", "plugin/**", "pom.xml", "b/**"}, Command: []string{"./mvnw", "-B", "-pl", "b", "test"}},
			},
		},
		"gradle groovy, root references, relative and root project paths": {
			files: map[string]string{
				"settings.gradle":      "include ':a',\n  'b' ; include(':a:c')\ninclude ':a'\n",
				"build.gradle":         "subprojects { dependencies { implementation project(':b') } }\nproject(':') {}\n",
				"a/build.gradle":       "dependencies { implementation project('c'); implementation findProject(path: \":b\") } // project(':x')\n",
				"b/src/B.java":         "",
				"a/c/build.gradle.kts": "val s = \"// not a comment \\\" still a string\"\n",
			},
			want: []gen.Unit{
				{ID: "a", Source: []string{"build.gradle", "settings.gradle", "a/**"}, Deps: []string{"a/c", "b"}, Command: []string{"gradle", ":a:test"}},
				{ID: "b", Source: []string{"build.gradle", "settings.gradle", "b/**"}, Command: []string{"gradle", ":b:test"}},
				{ID: "a/c", Source: []string{"build.gradle", "settings.gradle", "a/build.gradle", "a/c/**"}, Deps: []string{"a", "b"}, Command: []string{"gradle", ":a:c:test"}},
			},
		},
		"gradle intermediate project not included, source sets in the project": {
			files: map[string]string{
				"settings.gradle.kts":                `include(":common", ":services:web:app")`,
				"services/build.gradle.kts":          "subprojects { dependencies { \"implementation\"(project(\":common\")) } }\n",
				"services/web/app/build.gradle":      "sourceSets { main { java.srcDirs = ['src/gen', \"src/more\"]; resources.srcDir 'src/res' } }\n",
				"services/web/app/gradle.properties": "",
				"common/build.gradle.kts":            `sourceSets.main { kotlin.setSrcDirs(listOf("src/kt")) }`,
			},
			want: []gen.Unit{
				{ID: "common", Source: []string{"services/**", "settings.gradle.kts", "common/**"}, Command: []string{"gradle", ":common:test"}},
				{ID: "services/web/app", Source: []string{"services/**", "settings.gradle.kts", "services/build.gradle.kts", "services/web/app/**"}, Deps: []string{"common"}, Command: []string{"gradle", ":services:web:app:test"}},
			},
		},
		"maven parent by relative path, a module or from a repository": {
			files: map[string]string{
				"pom.xml":         pomHead + `<modules><module>parent</module><module>a</module><module>b</module><module>c</module></modules></project>`,
				"parent/pom.xml":  `<project><groupId>g</groupId><artifactId>parent</artifactId></project>`,
				"a/pom.xml":       `<project><parent><groupId>g</groupId><artifactId>parent</artifactId><relativePath>../parent</relativePath></parent><artifactId>a</artifactId></project>`,
				"b/pom.xml":       `<project><parent><groupId>x</groupId><artifactId>ext</artifactId><relativePath>../../outside</relativePath></parent><groupId>g</groupId><artifactId>b</artifactId></project>`,
				"c/pom.xml":       `<project><parent><groupId>x</groupId><artifactId>ext</artifactId><relativePath/></parent><groupId>g</groupId><artifactId>c</artifactId></project>`,
				"b/other/pom.xml": "not read",
			},
			want: []gen.Unit{
				{ID: "parent", Source: []string{"pom.xml", "parent/**"}, Command: []string{"mvn", "-B", "-pl", "parent", "test"}},
				{ID: "a", Source: []string{"pom.xml", "a/**"}, Deps: []string{"parent"}, Command: []string{"mvn", "-B", "-pl", "a", "test"}},
				{ID: "b", Source: []string{"pom.xml", "b/**"}, Command: []string{"mvn", "-B", "-pl", "b", "test"}},
				{ID: "c", Source: []string{"pom.xml", "c/**"}, Command: []string{"mvn", "-B", "-pl", "c", "test"}},
			},
		},
		"maven references outside dependencies and plugins": {
			files: map[string]string{
				"pom.xml": pomHead + `<modules><module>a</module><module>b</module><module>proc</module><module>ext</module></modules></project>`,
				"a/pom.xml": `<project><groupId>g</groupId><artifactId>a</artifactId><build><plugins><plugin><artifactId>maven-compiler-plugin</artifactId>
					<configuration><annotationProcessorPaths><path><groupId>g</groupId><artifactId>proc</artifactId></path></annotationProcessorPaths></configuration></plugin></plugins></build>
					<dependencies><dependency><groupId>x</groupId><artifactId>lib</artifactId><exclusions><exclusion><groupId>g</groupId><artifactId>b</artifactId></exclusion></exclusions></dependency></dependencies></project>`,
				"b/pom.xml":    `<project><groupId>g</groupId><artifactId>b</artifactId><build><extensions><extension><groupId>g</groupId><artifactId>ext</artifactId></extension></extensions></build></project>`,
				"proc/pom.xml": `<project><groupId>g</groupId><artifactId>proc</artifactId><reporting><plugins><plugin><groupId>g</groupId><artifactId>ext</artifactId></plugin></plugins></reporting></project>`,
				"ext/pom.xml":  `<project><groupId>g</groupId><artifactId>ext</artifactId></project>`,
			},
			want: []gen.Unit{
				{ID: "a", Source: []string{"pom.xml", "a/**"}, Deps: []string{"proc"}, Command: []string{"mvn", "-B", "-pl", "a", "test"}},
				{ID: "b", Source: []string{"pom.xml", "b/**"}, Deps: []string{"ext"}, Command: []string{"mvn", "-B", "-pl", "b", "test"}},
				{ID: "proc", Source: []string{"pom.xml", "proc/**"}, Deps: []string{"ext"}, Command: []string{"mvn", "-B", "-pl", "proc", "test"}},
				{ID: "ext", Source: []string{"pom.xml", "ext/**"}, Command: []string{"mvn", "-B", "-pl", "ext", "test"}},
			},
		},
		"root references are inherited by every module, resolved in its context": {
			files: inherited,
			want: []gen.Unit{
				{ID: "common", Source: []string{"pom.xml", "common/**"}, Deps: []string{"lint"}, Command: []string{"mvn", "-B", "-pl", "common", "test"}},
				{ID: "util", Source: []string{"pom.xml", "util/**"}, Deps: []string{"common", "lint"}, Command: []string{"mvn", "-B", "-pl", "util", "test"}},
				{ID: "lint", Source: []string{"pom.xml", "lint/**"}, Deps: []string{"common"}, Command: []string{"mvn", "-B", "-pl", "lint", "test"}},
				{ID: "tools", Source: []string{"pom.xml", "tools/**"}, Deps: []string{"common", "lint"}, Command: []string{"mvn", "-B", "-pl", "tools", "test"}},
				{ID: "bom", Source: []string{"pom.xml", "bom/**"}, Deps: []string{"common", "lint"}, Command: []string{"mvn", "-B", "-pl", "bom", "test"}},
				{ID: "app", Source: []string{"pom.xml", "app/**"}, Deps: []string{"bom", "common", "lint", "util"}, Command: []string{"mvn", "-B", "-pl", "app", "test"}},
			},
		},
		"root ${project.artifactId} reference, stray POMs in build output or unparsable": {
			files: map[string]string{
				"pom.xml":              pomHead + `<modules><module>a</module><module>a-fixtures</module></modules><dependencies><dependency><groupId>g</groupId><artifactId>${project.artifactId}-fixtures</artifactId><scope>test</scope></dependency></dependencies></project>`,
				"a/pom.xml":            `<project><groupId>g</groupId><artifactId>a</artifactId><dependencies><dependency><groupId>g</groupId><artifactId>x</artifactId></dependency></dependencies></project>`,
				"a-fixtures/pom.xml":   `<project><groupId>g</groupId><artifactId>a-fixtures</artifactId></project>`,
				"a/target/x/pom.xml":   `<project><groupId>g</groupId><artifactId>x</artifactId></project>`,
				"a/src/it/bad/pom.xml": `<project>`,
			},
			want: []gen.Unit{
				{ID: "a", Source: []string{"pom.xml", "a/**"}, Deps: []string{"a-fixtures"}, Command: []string{"mvn", "-B", "-pl", "a", "test"}},
				{ID: "a-fixtures", Source: []string{"pom.xml", "a-fixtures/**"}, Command: []string{"mvn", "-B", "-pl", "a-fixtures", "test"}},
			},
		},
		"gradle buildSrc, task paths, include in a string": {
			files: conventions,
			want: []gen.Unit{
				{ID: "a", Source: []string{"build.gradle.kts", "buildSrc/**", "config/**", "settings.gradle.kts", "a/**"}, Deps: []string{"b", "e"}, Command: []string{"gradle", ":a:test"}},
				{ID: "b", Source: []string{"build.gradle.kts", "buildSrc/**", "config/**", "settings.gradle.kts", "b/**"}, Deps: []string{"e"}, Command: []string{"gradle", ":b:test"}},
				{ID: "c", Source: []string{"build.gradle.kts", "buildSrc/**", "config/**", "settings.gradle.kts", "c/**"}, Deps: []string{"b", "d", "e"}, Command: []string{"gradle", ":c:test"}},
				{ID: "d", Source: []string{"build.gradle.kts", "buildSrc/**", "config/**", "settings.gradle.kts", "d/**"}, Deps: []string{"b", "e"}, Command: []string{"gradle", ":d:test"}},
				{ID: "e", Source: []string{"build.gradle.kts", "buildSrc/**", "config/**", "settings.gradle.kts", "e/**"}, Deps: []string{"b"}, Command: []string{"gradle", ":e:test"}},
			},
		},
		"imported local BOM and managed plugin are edges, managed dependency is not": {
			files: map[string]string{
				"pom.xml":     pomHead + `<modules><module>bom</module><module>lib</module><module>app</module></modules></project>`,
				"bom/pom.xml": `<project><groupId>g</groupId><artifactId>bom</artifactId></project>`,
				"lib/pom.xml": `<project><groupId>g</groupId><artifactId>lib</artifactId></project>`,
				"app/pom.xml": `<project><groupId>g</groupId><artifactId>app</artifactId><dependencyManagement><dependencies>
					<dependency><groupId>g</groupId><artifactId>bom</artifactId><type>pom</type><scope>import</scope></dependency>
					<dependency><groupId>g</groupId><artifactId>lib</artifactId><scope>test</scope></dependency></dependencies></dependencyManagement>
					<build><pluginManagement><plugins><plugin><groupId>g</groupId><artifactId>lib</artifactId><scope>import</scope></plugin></plugins></pluginManagement></build></project>`,
			},
			want: []gen.Unit{
				{ID: "bom", Source: []string{"pom.xml", "bom/**"}, Command: []string{"mvn", "-B", "-pl", "bom", "test"}},
				{ID: "lib", Source: []string{"pom.xml", "lib/**"}, Command: []string{"mvn", "-B", "-pl", "lib", "test"}},
				{ID: "app", Source: []string{"pom.xml", "app/**"}, Deps: []string{"bom", "lib"}, Command: []string{"mvn", "-B", "-pl", "app", "test"}},
			},
		},
	} {
		got, err := Adapter{}.Units(tree(t, tc.files))
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v, %v\nwant %+v", name, got, err, tc.want)
		}
	}
}

// Anything the adapter cannot read with certainty is an error, so aval gen
// writes nothing (invariant 3).
func TestUnitsFailClosed(t *testing.T) {
	sub := func(body string) map[string]string {
		return map[string]string{"pom.xml": pomHead + `<modules><module>a</module></modules></project>`, "a/pom.xml": body}
	}
	two := func(body string) map[string]string { // modules a and common, body in a
		return map[string]string{
			"pom.xml":        pomHead + `<modules><module>a</module><module>common</module></modules></project>`,
			"a/pom.xml":      `<project><groupId>g</groupId><artifactId>a</artifactId>` + body + `</project>`,
			"common/pom.xml": `<project><groupId>g</groupId><artifactId>common</artifactId></project>`,
		}
	}
	kts := func(settings string, files ...string) map[string]string {
		m := map[string]string{"settings.gradle.kts": settings}
		for i := 0; i+1 < len(files); i += 2 {
			m[files[i]] = files[i+1]
		}
		return m
	}
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"no build":             {map[string]string{"README": ""}, "has no pom.xml, settings.gradle.kts or settings.gradle"},
		"maven and gradle":     {map[string]string{"pom.xml": "", "settings.gradle": ""}, "both pom.xml and settings.gradle"},
		"two settings":         {map[string]string{"settings.gradle": "", "settings.gradle.kts": ""}, "both settings.gradle.kts and settings.gradle"},
		"bad xml":              {map[string]string{"pom.xml": "<project>"}, "pom.xml: XML syntax error"},
		"not a pom":            {map[string]string{"pom.xml": "<html/>"}, "the root element is <html>"},
		"missing module":       {map[string]string{"pom.xml": pomHead + `<modules><module>gone</module></modules></project>`}, "gone"},
		"module pom file":      {map[string]string{"pom.xml": pomHead + `<modules><module>a/pom.xml</module></modules></project>`}, "names a POM file"},
		"module outside":       {map[string]string{"pom.xml": pomHead + `<subprojects><subproject>../x</subproject></subprojects></project>`}, `"../x": want a directory inside the root`},
		"source in the module": {sub(`<project><groupId>g</groupId><artifactId>a</artifactId><build><testSourceDirectory>test</testSourceDirectory></build></project>`), ""},
		"resource outside":     {sub(`<project><profiles><profile><build><resources><resource><directory>../conf</directory></resource></resources></build></profile></profiles></project>`), "a/pom.xml: <directory>../conf</directory> refers to files outside the module directory"},
		"system path outside":  {sub(`<project><dependencies><dependency><systemPath>${basedir}/../lib/x.jar</systemPath></dependency></dependencies></project>`), "<systemPath>"},
		"absolute source":      {sub(`<project><build><plugins><plugin><configuration><sources><source>/opt/gen</source></sources></configuration></plugin></plugins></build></project>`), "<source>/opt/gen</source>"},
		"property set by a profile": {sub(`<project><groupId>g</groupId><artifactId>a</artifactId><properties><x>b</x></properties><profiles><profile><properties><x>c</x></properties></profile></profiles>
			<dependencies><dependency><groupId>g</groupId><artifactId>${x}</artifactId></dependency></dependencies></project>`), "a/pom.xml: property x set by a profile"},
		"property set by a root profile": {map[string]string{
			"pom.xml":   pomHead + `<modules><module>a</module></modules><profiles><profile><properties><x>c</x></properties></profile></profiles></project>`,
			"a/pom.xml": `<project><groupId>g</groupId><artifactId>a</artifactId><dependencies><dependency><groupId>g</groupId><artifactId>${x}</artifactId></dependency></dependencies></project>`,
		}, "property x set by a profile"},
		"reference to a POM outside the reactor": {map[string]string{
			"pom.xml":   pomHead + `<modules><module>a</module></modules></project>`,
			"a/pom.xml": `<project><groupId>g</groupId><artifactId>a</artifactId><dependencies><dependency><groupId>g</groupId><artifactId>x</artifactId></dependency></dependencies></project>`,
			"x/pom.xml": `<project><groupId>g</groupId><artifactId>x</artifactId></project>`,
		}, "a/pom.xml: g:x refers to x/pom.xml, a local POM outside the build"},
		"duplicate coordinates": {map[string]string{
			"pom.xml":   pomHead + `<modules><module>a</module><module>b</module></modules></project>`,
			"a/pom.xml": `<project><groupId>g</groupId><artifactId>x</artifactId></project>`,
			"b/pom.xml": `<project><groupId>g</groupId><artifactId>x</artifactId></project>`,
		}, "modules a and b are both g:x"},
		"unresolved own property":        {sub(`<project><groupId>${nope}</groupId><artifactId>a</artifactId></project>`), "a/pom.xml: cannot resolve the properties in ${nope}:a"},
		"unresolved dependency property": {sub(`<project><groupId>g</groupId><artifactId>a</artifactId><dependencies><dependency><groupId>g</groupId><artifactId>${x}</artifactId></dependency></dependencies></project>`), "cannot resolve the properties in g:${x}"},
		"unresolved root property":       {map[string]string{"pom.xml": pomHead + `<modules><module>a</module></modules><dependencies><dependency><groupId>g</groupId><artifactId>${y}</artifactId></dependency></dependencies></project>`, "a/pom.xml": `<project><groupId>g</groupId><artifactId>a</artifactId></project>`}, "a/pom.xml: cannot resolve the properties in g:${y}"},
		"bad property element":           {sub(`<project><properties><a>x</b></properties></project>`), "a/pom.xml: XML syntax error"},
		"unclosed properties":            {sub(`<project><properties>`), "a/pom.xml: XML syntax error"},
		"element after the project":      {sub(`<project/><x>`), "a/pom.xml: XML syntax error"},
		"skipped element after project":  {sub(`<project/><exclusions>`), "a/pom.xml: XML syntax error"},
		"parent outside the build": {map[string]string{
			"pom.xml":        pomHead + `<modules><module>a</module></modules></project>`,
			"a/pom.xml":      `<project><parent><groupId>g</groupId><artifactId>p</artifactId><relativePath>../parent/pom.xml</relativePath></parent><artifactId>a</artifactId></project>`,
			"parent/pom.xml": `<project><groupId>g</groupId><artifactId>p</artifactId></project>`,
		}, "a/pom.xml: parent parent/pom.xml is a local POM outside the build"},
		"default parent path outside the build": {map[string]string{
			"pom.xml":     pomHead + `<modules><module>x/a</module></modules></project>`,
			"x/a/pom.xml": `<project><parent><groupId>g</groupId><artifactId>p</artifactId></parent><artifactId>a</artifactId></project>`,
			"x/pom.xml":   `<project><groupId>g</groupId><artifactId>p</artifactId></project>`,
		}, "parent x/pom.xml is a local POM outside the build"},

		"include build":                        {kts(`pluginManagement { includeBuild("build-logic") }`), "settings.gradle.kts: includeBuild: not supported by aval gen java"},
		"project dir":                          {kts(`include(":a")` + "\n" + `project(":a").projectDir = file("x")`), "projectDir"},
		"computed include":                     {kts(`listOf("a").forEach { include(it) }`), "include: an argument other than a string literal"},
		"concatenated":                         {kts(`include(":a" + suffix)`), "include: an argument other than a string literal"},
		"groovy concatenated":                  {map[string]string{"settings.gradle": "include ':a' + suffix\n"}, "include: an argument other than a string literal"},
		"interpolated":                         {kts(`include(":${name}")`), "a string with interpolation or escapes"},
		"unterminated":                         {kts(`include(":a`), "a string with interpolation or escapes"},
		"include outside":                      {kts(`include("..")`), `include ":..": ".."`},
		"two root builds":                      {kts(`include(":a")`, "build.gradle", "", "build.gradle.kts", ""), "both build.gradle.kts and build.gradle"},
		"two unit builds":                      {kts(`include(":a")`, "a/build.gradle", "", "a/build.gradle.kts", ""), "both build.gradle.kts and build.gradle"},
		"accessor in root":                     {kts(`include(":a")`, "build.gradle.kts", "dependencies { implementation(projects.a) }"), "build.gradle.kts: type-safe project accessors"},
		"computed project":                     {kts(`include(":a")`, "a/build.gradle.kts", "dependencies { implementation(project(name)) }"), "a/build.gradle.kts: project(...): an argument other than a string literal"},
		"apply from kotlin":                    {kts(`include(":a")`, "a/build.gradle.kts", `apply(from = "../gradle/shared.gradle.kts")`), "a/build.gradle.kts: apply from: not supported"},
		"apply from groovy":                    {kts(`include(":a")`, "build.gradle", "subprojects { apply from: '../x.gradle' }"), "build.gradle: apply from"},
		"source set in another project":        {kts(`include(":a", ":b")`, "a/build.gradle.kts", `sourceSets { main { java.srcDir("../b/gen") } }`), `a/build.gradle.kts: "../b/gen" refers to files of another project`},
		"source set in the root context":       {kts(`include(":a", ":b")`, "a/build.gradle.kts", `sourceSets { main { java.srcDir("../gen") } }; val c = file("$rootDir/config/x.xml")`), ""},
		"path outside the root":                {kts(`include(":a")`, "a/build.gradle.kts", `val x = file("../../x")`), `"../../x" refers to files outside the root`},
		"project dir prefix":                   {kts(`include(":a")`, "a/build.gradle.kts", `val x = file("$projectDir/../../x")`), "refers to files outside the root"},
		"source set in project":                {kts(`include(":a")`, "a/build.gradle.kts", `sourceSets { main { java.srcDir("gen") }; java.srcDir(layout.buildDirectory.dir("gen")) }; val p = "a.b".replace('.', '/')`), ""},
		"absolute file":                        {kts(`include(":a")`, "a/build.gradle.kts", `dependencies { implementation(files("/opt/x.jar")) }`), `"/opt/x.jar" is an absolute path`},
		"root dir into a project":              {kts(`include(":a", ":b")`, "a/build.gradle.kts", `val c = file("${rootDir}/b/x.xml")`), `"${rootDir}/b/x.xml" refers to files of another project`},
		"root dir outside":                     {kts(`include(":a")`, "build.gradle.kts", `val c = file("$rootDir/../x")`), "refers to files outside the root"},
		"parent from a subproject":             {kts(`include(":a", ":b")`, "a/build.gradle.kts", `val s = parent!!.childProjects`), "a/build.gradle.kts: parent refers to files of another project"},
		"root subprojects":                     {kts(`include(":a", ":b")`, "a/build.gradle.kts", `rootProject.subprojects { }`), "rootProject.subprojects refers to files of another project"},
		"parent file in the root":              {kts(`include(":a")`, "build.gradle.kts", `val d = projectDir.parentFile`), "build.gradle.kts: parentFile refers"},
		"triple-quoted string":                 {kts(`include(":a", ":b")`, "a/build.gradle.kts", `val x = """project(":b")"""`), "a/build.gradle.kts: a triple-quoted string"},
		"triple-quoted settings":               {kts(`include(":a")` + "\nval x = '''y'''"), "settings.gradle.kts: a triple-quoted string"},
		"root project file into a project":     {kts(`include(":a", ":common")`, "a/build.gradle.kts", `val f = rootProject.file("common/fixtures")`), `a/build.gradle.kts: "common/fixtures" refers to files of another project`},
		"root dir resolve into a project":      {kts(`include(":a", ":common")`, "a/build.gradle.kts", `val f = rootDir.resolve("common/fixtures")`), `"common/fixtures" refers to files of another project`},
		"settings directory from buildSrc":     {kts(`include(":a", ":common")`, "buildSrc/src/main/kotlin/c.gradle.kts", `val f = layout.settingsDirectory.dir("common/fixtures")`), `buildSrc/src/main/kotlin/c.gradle.kts: "common/fixtures" refers to files of another project`},
		"root file inside subprojects":         {kts(`include(":a", ":common")`, "build.gradle.kts", `subprojects { val f = file("common/fixtures") }`), `build.gradle.kts: "common/fixtures" refers to files of another project`},
		"nested project from its parent":       {kts(`include(":s", ":s:a")`, "s/build.gradle.kts", `val f = file("s/a/x")`), ""},
		"root project children":                {kts(`include(":a", ":b")`, "a/build.gradle.kts", `project(":").subprojects { }`), `a/build.gradle.kts: project(":").subprojects refers to files of another project`},
		"root project children by path":        {kts(`include(":a", ":b")`, "a/build.gradle.kts", `val c = project(path = ":").getChildProjects()`), "getChildProjects refers to files of another project"},
		"root project children in the root":    {kts(`include(":a")`, "build.gradle.kts", `project(":").subprojects { }`), ""},
		"dot-dot inside an argument":           {kts(`include(":a")`, "a/build.gradle.kts", `tasks.test { jvmArgs("-Dfx=../common") }`), `"-Dfx=../common" holds a path inside larger text`},
		"root dir inside an argument":          {kts(`include(":a")`, "a/build.gradle.kts", `tasks.test { jvmArgs("-Dfx=$rootDir/common") }`), "holds a path inside larger text"},
		"root dir then dot-dot":                {kts(`include(":a")`, "a/build.gradle.kts", `val f = file("${rootDir}/config/../../x")`), "holds a path inside larger text"},
		"literals that are not paths":          {kts(`include(":a", ":common")`, "a/build.gradle.kts", `val n = "${rootProject.name}-x"; val c = "common"; val u = "https://repo/common/x"; val g = "com.acme:lib:1.0"; val s = "src/main/x"`), ""},
		"maven env":                            {sub(`<project><properties><d>${env.HOME}/x</d></properties></project>`), "${env.HOME}"},
		"maven grandparent basedir":            {sub(`<project><properties><d>${project.parent.parent.basedir}/x</d></properties></project>`), "${project.parent.parent.basedir}"},
		"maven argLine":                        {sub(`<project><build><plugins><plugin><configuration><argLine>-Xmx1g -Dfx=../common/fixtures</argLine></configuration></plugin></plugins></build></project>`), "<argLine>-Xmx1g -Dfx=../common/fixtures</argLine>"},
		"maven parent coordinates":             {sub(`<project><groupId>g</groupId><artifactId>a</artifactId><version>${project.parent.version}</version></project>`), ""},
		"bare project dir in rootProject.file": {kts(`include(":a", ":common")`, "a/build.gradle.kts", `tasks.test { inputs.dir(rootProject.file("common")) }`), `a/build.gradle.kts: "common" refers to files of another project`},
		"bare project dir in resolve":          {kts(`include(":a", ":common")`, "a/build.gradle.kts", `val f = rootDir.resolve("common")`), `"common" refers to files of another project`},
		"bare project dir in root files":       {kts(`include(":a", ":common")`, "build.gradle.kts", `subprojects { dependencies { "testImplementation"(files("common")) } }`), `build.gradle.kts: "common" refers to files of another project`},
		"bare project dir in buildSrc":         {kts(`include(":a", ":common")`, "buildSrc/src/main/kotlin/c.gradle.kts", `val t = fileTree("common")`), `buildSrc/src/main/kotlin/c.gradle.kts: "common" refers to files`},
		"bare project dir second argument":     {kts(`include(":a", ":common")`, "a/build.gradle.kts", `tasks.register<Copy>("c") { from("src", "common"); into(layout.buildDirectory.dir("x")) }`), `"common" refers to files of another project`},
		"bare project names elsewhere":         {kts(`include(":a", ":common")`+"\n"+`include("common")`, "a/build.gradle.kts", `val c = "common"; group = "common"; dependencies { implementation(project(":common")) }; val own = file("a"); val f = file(name)`), ""},
		"jvm.config property":                  {map[string]string{".mvn/jvm.config": "-Xmx1g -Dlib=x", "pom.xml": pomHead + `<properties><lib>a</lib></properties><modules><module>a</module></modules><dependencies><dependency><groupId>g</groupId><artifactId>${lib}</artifactId></dependency></dependencies></project>`, "a/pom.xml": `<project><groupId>g</groupId><artifactId>a</artifactId></project>`}, "a/pom.xml: property lib set by .mvn/jvm.config"},
		"unreadable jvm.config":                {map[string]string{".mvn/jvm.config/x": "", "pom.xml": pomHead + `</project>`}, "jvm.config"},
		"File with a project dir":              {kts(`include(":a", ":common")`, "a/build.gradle.kts", `val f = File(rootDir, "common")`), `a/build.gradle.kts: "common" refers to files of another project`},
		"new File with a project dir":          {kts(`include(":a", ":common")`, "build.gradle", `def f = new File("common")`), `build.gradle: "common" refers to files of another project`},
		"unterminated path call":               {kts(`include(":a", ":common")`, "a/build.gradle.kts", `val f = file("common"`), `"common" refers to files of another project`},
		"File with a root dir":                 {kts(`include(":a", ":common")`, "a/build.gradle.kts", `val f = File(rootDir, "config"); val g = file(listOf("x", "y").joinToString("/"))`), ""},
		"maven root property, root context":    {two(`<properties><cfg>${maven.multiModuleProjectDirectory}/config/c.xml</cfg><gpg.passphrase>${env.GPG_PASSPHRASE}</gpg.passphrase><out>gen</out><rel>dist/${project.parent.version}/x</rel></properties><build><directory>${project.build.directory}/x</directory><outputDirectory>${out}/y</outputDirectory></build>`), ""},
		"maven root property into a module":    {two(`<properties><cfg>${maven.multiModuleProjectDirectory}/common/c.xml</cfg></properties>`), "a/pom.xml: <cfg>${maven.multiModuleProjectDirectory}/common/c.xml</cfg>"},
		"maven user.dir into a module":         {two(`<properties><d>${user.dir}/common/x</d></properties>`), "<d>${user.dir}/common/x</d>"},
		"maven 4 rootDirectory into a module":  {two(`<properties><d>${rootDirectory}/common</d></properties>`), "${rootDirectory}/common"},
		"maven root property outside":          {two(`<properties><d>${rootDirectory}/../x</d></properties>`), "${rootDirectory}/../x"},
		"maven session path":                   {sub(`<project><build><directory>${session.request.baseDirectory}/x</directory></build></project>`), "${session.request.baseDirectory}/x"},
		"maven env inside a path":              {sub(`<project><properties><d>/opt/${env.X}</d></properties></project>`), "<d>/opt/${env.X}</d>"},
		"maven pom.parent":                     {sub(`<project><properties><d>${pom.parent.basedir}/x</d></properties></project>`), "${pom.parent.basedir}/x"},
		"maven backslash dot-dot":              {sub(`<project><properties><d>-Dfx=..\shared\x</d></properties></project>`), `<d>-Dfx=..\shared\x</d>`},
		"maven run-time property":              {two(`<build><plugins><plugin><configuration><configLocation>${main.basedir}/common/checkstyle.xml</configLocation></configuration></plugin></plugins></build>`), "<configLocation>${main.basedir}/common/checkstyle.xml</configLocation>"},
		"maven property set to the root":       {two(`<properties><main.dir>${maven.multiModuleProjectDirectory}</main.dir><cfg>${main.dir}/config/x</cfg></properties>`), "<cfg>${main.dir}/config/x</cfg>"},
		"maven relative path into a module":    {two(`<build><plugins><plugin><configuration><configLocation>common/checkstyle.xml</configLocation></configuration></plugin></plugins></build>`), "<configLocation>common/checkstyle.xml</configLocation>"},
		"android compose template": {map[string]string{
			"settings.gradle.kts":       "pluginManagement {\n    repositories {\n        google {\n            content {\n                includeGroupByRegex(\"com\\\\.android.*\")\n            }\n        }\n        mavenCentral()\n        gradlePluginPortal()\n    }\n}\ndependencyResolutionManagement {\n    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)\n    repositories {\n        google()\n        mavenCentral()\n    }\n}\n\nrootProject.name = \"My Application\"\ninclude(\":app\")\n",
			"build.gradle.kts":          "plugins {\n    alias(libs.plugins.android.application) apply false\n    alias(libs.plugins.kotlin.android) apply false\n    alias(libs.plugins.kotlin.compose) apply false\n}\n",
			"app/build.gradle.kts":      "plugins {\n    alias(libs.plugins.android.application)\n}\nandroid {\n    namespace = \"com.example.myapplication\"\n    compileSdk = 35\n    buildTypes {\n        release {\n            isMinifyEnabled = false\n            proguardFiles(getDefaultProguardFile(\"proguard-android-optimize.txt\"), \"proguard-rules.pro\")\n        }\n    }\n    packaging {\n        resources {\n            excludes += \"/META-INF/{AL2.0,LGPL2.1}\"\n        }\n    }\n}\ntasks.withType<Jar> { exclude(\"/META-INF/*\") }\ndependencies {\n    implementation(libs.androidx.core.ktx)\n}\n",
			"gradle/libs.versions.toml": "",
		}, ""},
		"root rootDir receiver":       {kts(`include(":a", ":common")`, "a/build.gradle.kts", `val f = file("${project.rootProject.rootDir}/common/x")`), `"${project.rootProject.rootDir}/common/x" refers to files of another project`},
		"plugin rootDir receiver":     {kts(`include(":a", ":common")`, "buildSrc/src/main/kotlin/P.kt", `class P { fun apply(target: Project) { val f = target.file("${target.rootDir}/common/x") } }`), "refers to files of another project"},
		"rootDir receiver in text":    {kts(`include(":a")`, "a/build.gradle.kts", `tasks.test { jvmArgs("-Dfx=${target.rootDir}/y") }`), "holds a path inside larger text"},
		"backslash dot-dot in text":   {kts(`include(":a")`, "a/build.gradle.kts", `tasks.test { jvmArgs("-Dfx=..\\common") }`), "holds a path inside larger text"},
		"resolve sibling":             {kts(`include(":a")`, "a/build.gradle.kts", `val f = projectDir.resolveSibling("b")`), "a/build.gradle.kts: resolveSibling"},
		"root resolve chain":          {kts(`include(":a", ":services:api")`, "a/build.gradle.kts", `val f = rootDir.resolve("services").resolve("api")`), `a/build.gradle.kts: rootDir.resolve(...: refers to files of another project`},
		"computed root resolve":       {kts(`include(":a")`, "a/build.gradle.kts", `val f = rootDir.resolve(name)`), "rootDir.resolve(...: an argument other than a string literal"},
		"root resolve outside":        {kts(`include(":a")`, "a/build.gradle.kts", `val f = rootProject.projectDir.resolve("..").resolve("x")`), "refers to files outside the root"},
		"settings directory chain":    {kts(`include(":a")`, "a/build.gradle.kts", `val f = layout.settingsDirectory.dir("config").file("x.xml"); val g = rootDir.resolve("config"`), ""},
		"maven filter":                {sub(`<project><build><filters><filter>../shared/f.properties</filter></filters></build></project>`), "<filter>../shared/f.properties</filter>"},
		"maven suiteXmlFile":          {sub(`<project><build><plugins><plugin><configuration><suiteXmlFiles><suiteXmlFile>../testng.xml</suiteXmlFile></suiteXmlFiles></configuration></plugin></plugins></build></project>`), "<suiteXmlFile>"},
		"maven activation file":       {sub(`<project><profiles><profile><activation><file><exists>${basedir}/../x</exists></file></activation></profile></profiles></project>`), "<exists>"},
		"maven property path":         {sub(`<project><properties><d>../shared</d></properties></project>`), "<d>../shared</d>"},
		"maven.config property":       {map[string]string{".mvn/maven.config": "-B -D a=1 --define=b=2 -Dlib=x --define", "pom.xml": pomHead + `<properties><lib>a</lib></properties><modules><module>a</module></modules><dependencies><dependency><groupId>g</groupId><artifactId>${lib}</artifactId></dependency></dependencies></project>`, "a/pom.xml": `<project><groupId>g</groupId><artifactId>a</artifactId></project>`}, "a/pom.xml: property lib set by .mvn/maven.config"},
		"maven.config other property": {map[string]string{".mvn/maven.config": "-Dother=x", "pom.xml": pomHead + `<properties><lib>a</lib></properties><modules><module>a</module></modules><dependencies><dependency><groupId>g</groupId><artifactId>${lib}</artifactId></dependency></dependencies></project>`, "a/pom.xml": `<project><groupId>g</groupId><artifactId>a</artifactId></project>`}, ""},
		"unreadable maven.config":     {map[string]string{".mvn/maven.config/x": "", "pom.xml": pomHead + `</project>`}, "maven.config"},
		"apply from in settings":      {kts(`include(":a")` + "\n" + `apply(from = "more.settings.gradle.kts")`), "settings.gradle.kts: apply from"},
		"empty project segment":       {kts(`include(":a")`, "a/build.gradle.kts", `evaluationDependsOn("::")`), `a/build.gradle.kts: project path "::"`},
		"computed evaluation":         {kts(`include(":a")`, "a/build.gradle.kts", `evaluationDependsOn(other)`), "a/build.gradle.kts: evaluationDependsOn(...): an argument other than a string literal"},
		"buildSrc computed ref":       {kts(`include(":a")`, "buildSrc/src/main/kotlin/c.gradle.kts", `dependencies { "implementation"(project(name)) }`), "buildSrc/src/main/kotlin/c.gradle.kts: project(...)"},
		"ancestor two builds":         {kts(`include(":s:a")`, "s/build.gradle", "", "s/build.gradle.kts", ""), "both build.gradle.kts and build.gradle"},
		"ancestor computed ref":       {kts(`include(":s:a")`, "s/build.gradle.kts", "subprojects { dependencies { implementation(project(name)) } }"), "s/build.gradle.kts: project(...)"},
		"comment hides rest":          {kts("include(\":a\") /* unterminated\ninclude(name)"), ""},
		"comment at the end":          {kts(`include(":a") // no line break after it`), ""},
	} {
		_, err := Adapter{}.Units(tree(t, tc.files))
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: error %v, want %q", name, err, tc.want)
		}
	}
}

func TestUnreadableScript(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	gradle := map[string]string{"settings.gradle.kts": `include(":a")`, "a/build.gradle.kts": "", "buildSrc/c.kt": "", "buildSrc/d/e.kt": ""}
	maven := map[string]string{"pom.xml": pomHead + `<modules><module>a</module></modules></project>`, "a/pom.xml": `<project><groupId>g</groupId><artifactId>a</artifactId></project>`, "x/pom.xml": "", "y/z/pom.xml": ""}
	for file, files := range map[string]map[string]string{
		"settings.gradle.kts": gradle, "a/build.gradle.kts": gradle, "buildSrc/c.kt": gradle, "buildSrc/d": gradle,
		"x/pom.xml": maven, "y": maven,
	} {
		root := tree(t, files)
		p := filepath.Join(root, filepath.FromSlash(file))
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
		if _, err := (Adapter{}).Units(root); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("%s unreadable: %v", file, err)
		}
	}
	for _, files := range []map[string]string{gradle, maven} { // the build files are readable, the root not listable
		root := tree(t, files)
		if err := os.Chmod(root, 0o311); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
		if _, err := (Adapter{}).Units(root); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("root not listable: %v", err)
		}
	}
}

// The Gradle script reader never panics, and an include it accepts names
// only the literals in the script.
func FuzzGradleSettings(f *testing.F) {
	for _, s := range []string{`include(":a", ":b",)`, "include ':a',\n ':b'", `include(":${x}")`, "/* x */ include('a') // y", `include("`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		paths, err := includes(stripComments(s))
		for _, p := range paths {
			if err == nil && !strings.Contains(s, p) {
				t.Fatalf("include %q is not in %q", p, s)
			}
		}
	})
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// The POM reader never panics, and every artifactId it returns is text of
// the input (checked when the input has no entities, comments or CDATA,
// which change the text).
func FuzzPom(f *testing.F) {
	for _, s := range []string{
		pomHead + `<dependencies><dependency><groupId>g</groupId><artifactId>a</artifactId></dependency></dependencies></project>`,
		`<project><dependencyManagement><dependencies><dependency><artifactId>b</artifactId><scope>import</scope></dependency></dependencies></dependencyManagement></project>`,
		`<project><properties><artifactId>x</artifactId></properties><build><directory>../t</directory></build></project>`,
		`<project><artifactId>a<!--c-->b</artifactId>`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		var p pom
		_ = xml.Unmarshal([]byte(s), &p)
		refs, _, err := coordRefs([]byte(s))
		if err != nil || strings.Contains(s, "&") || strings.Contains(s, "<!") {
			return
		}
		text := tagRe.ReplaceAllString(s, "")
		for _, c := range refs {
			if !strings.Contains(text, c.ArtifactID) {
				t.Fatalf("artifactId %q is not in %q", c.ArtifactID, s)
			}
		}
	})
}

// The build script reader never panics, and every unit it returns is named
// by a literal of the script.
func FuzzGradleRefs(f *testing.F) {
	for _, s := range []string{
		`dependencies { implementation(project(":a")) }`,
		`implementation findProject(path: 'b'); evaluationDependsOn(":a")`,
		`tasks.test { dependsOn(":a:b:jar") }`,
		`val x = "$rootDir/../y"`,
		`project(":${x}")`,
	} {
		f.Add(s)
	}
	known := map[string]bool{"a": true, "a/b": true, "b": true}
	f.Fuzz(func(t *testing.T, s string) {
		ids, err := scriptRefs(stripComments(s), "build.gradle.kts", ":", known)
		for _, id := range ids {
			if err == nil && !strings.Contains(s, path.Base(id)) {
				t.Fatalf("unit %q is not in %q", id, s)
			}
		}
	})
}

// A module or project directory named with glob metacharacters gets an
// escaped glob, so a change to its files changes its test's fingerprint and
// no other; a name no contract glob can express fails closed.
func TestFalseHitMetacharacterDirectory(t *testing.T) {
	set := []byte("apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\n")
	for _, files := range []map[string]string{
		{"pom.xml": pomHead + `<modules><module>a[1]</module><module>a1</module></modules></project>`,
			"a[1]/pom.xml": `<project><groupId>g</groupId><artifactId>x</artifactId></project>`, "a[1]/A.java": "",
			"a1/pom.xml": `<project><groupId>g</groupId><artifactId>y</artifactId></project>`},
		{"settings.gradle.kts": `include(":a[1]", ":a1")`, "a[1]/A.java": "", "a1/B.java": ""},
	} {
		out := generate(t, tree(t, files), set)
		if !strings.Contains(string(out), "a[[]1]/**") {
			t.Fatalf("no escaped glob:\n%s", out)
		}
		fsys := fstest.MapFS{"aval.golden.yaml": {Data: out}}
		for k, v := range files {
			fsys[k] = &fstest.MapFile{Data: []byte(v)}
		}
		changed := maps.Clone(fsys)
		changed["a[1]/A.java"] = &fstest.MapFile{Data: []byte("changed")}
		if digest(t, changed, "java.a-1") == digest(t, fsys, "java.a-1") || digest(t, changed, "java.a1") != digest(t, fsys, "java.a1") {
			t.Errorf("%v: want only java.a-1 to change", files)
		}
	}
	if runtime.GOOS == "windows" { // no backslash in a file name
		return
	}
	for files, want := range map[*map[string]string]string{
		{"pom.xml": pomHead + `<modules><module>b\c</module></modules></project>`, `b\c/pom.xml`: `<project><groupId>g</groupId><artifactId>b</artifactId></project>`}: `"b\\c": a contract glob cannot express`,
		{"settings.gradle.kts": `include(":a")`, `x\y`: ""}: `"x\\y": a contract glob cannot express`,
	} {
		if _, err := (Adapter{}).Units(tree(t, *files)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}
