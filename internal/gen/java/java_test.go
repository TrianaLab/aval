package java

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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
		build, test, transitive, unrelated, shared, parent string
	}{
		// web -> api -> inventory -> common; tools stands alone.
		{"maven", "java.services.web", "common/src/main/java/com/acme/common/Money.java", "tools/src/main/java/com/acme/tools/Gen.java", "pom.xml", "services/pom.xml"},
		// web -> api -> inventory -> common; services/build.gradle.kts configures web.
		{"gradle", "java.services.web", "common/src/main/kotlin/com/acme/common/Money.kt", "gradlew", "gradle/libs.versions.toml", "services/build.gradle.kts"},
	} {
		fsys := fixture(t, tc.build)
		before := digest(t, fsys, tc.test)
		for file, reuse := range map[string]bool{tc.transitive: false, tc.unrelated: true, tc.shared: false, tc.parent: false} {
			changed := fstest.MapFS{}
			for k, v := range fsys {
				changed[k] = v
			}
			changed[file] = &fstest.MapFile{Data: append(read(t, filepath.Join("testdata", tc.build, file)), "// changed\n"...)}
			if got := digest(t, changed, tc.test) == before; got != reuse {
				t.Errorf("%s: %s after changing %s: same fingerprint %v, want %v", tc.build, tc.test, file, got, reuse)
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
				{ID: "a", Source: []string{"a/pom.xml", "a/src/**"}, Deps: []string{"b"}, Command: []string{"./mvnw", "-B", "-pl", "a", "test"}},
				{ID: "b", Source: []string{"b/pom.xml"}, Command: []string{"./mvnw", "-B", "-pl", "b", "test"}},
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
				{ID: "a", Source: []string{"a/build.gradle"}, Deps: []string{"a/c", "b", "b"}, Command: []string{"gradle", ":a:test"}},
				{ID: "b", Source: []string{"b/src/**"}, Command: []string{"gradle", ":b:test"}},
				{ID: "a/c", Source: []string{"a/build.gradle", "a/c/build.gradle.kts"}, Deps: []string{"b", "b"}, Command: []string{"gradle", ":a:c:test"}},
			},
		},
		"gradle intermediate project not included, source sets under src, gradle.properties": {
			files: map[string]string{
				"settings.gradle.kts":                `include(":common", ":services:web:app")`,
				"services/build.gradle.kts":          "subprojects { dependencies { \"implementation\"(project(\":common\")) } }\n",
				"services/web/app/build.gradle":      "sourceSets { main { java.srcDirs = ['src/gen', \"src/more\"]; resources.srcDir 'src/res' } }\n",
				"services/web/app/gradle.properties": "",
				"common/build.gradle.kts":            `sourceSets.main { kotlin.setSrcDirs(listOf("src/kt")) }`,
			},
			want: []gen.Unit{
				{ID: "common", Source: []string{"common/build.gradle.kts"}, Command: []string{"gradle", ":common:test"}},
				{ID: "services/web/app", Source: []string{"services/build.gradle.kts", "services/web/app/build.gradle", "services/web/app/gradle.properties"}, Deps: []string{"common"}, Command: []string{"gradle", ":services:web:app:test"}},
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
				{ID: "parent", Source: []string{"parent/pom.xml"}, Command: []string{"mvn", "-B", "-pl", "parent", "test"}},
				{ID: "a", Source: []string{"a/pom.xml"}, Deps: []string{"parent"}, Command: []string{"mvn", "-B", "-pl", "a", "test"}},
				{ID: "b", Source: []string{"b/pom.xml"}, Command: []string{"mvn", "-B", "-pl", "b", "test"}},
				{ID: "c", Source: []string{"c/pom.xml"}, Command: []string{"mvn", "-B", "-pl", "c", "test"}},
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
				{ID: "a", Source: []string{"a/pom.xml"}, Deps: []string{"proc"}, Command: []string{"mvn", "-B", "-pl", "a", "test"}},
				{ID: "b", Source: []string{"b/pom.xml"}, Deps: []string{"ext"}, Command: []string{"mvn", "-B", "-pl", "b", "test"}},
				{ID: "proc", Source: []string{"proc/pom.xml"}, Deps: []string{"ext"}, Command: []string{"mvn", "-B", "-pl", "proc", "test"}},
				{ID: "ext", Source: []string{"ext/pom.xml"}, Command: []string{"mvn", "-B", "-pl", "ext", "test"}},
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
		"module glob":          {map[string]string{"pom.xml": pomHead + `<modules><module>a*</module></modules></project>`}, "without glob characters"},
		"source outside src":   {sub(`<project><build><testSourceDirectory>test</testSourceDirectory></build></project>`), `"test" outside src`},
		"resource outside src": {sub(`<project><profiles><profile><build><resources><resource><directory>../conf</directory></resource></resources></build></profile></profiles></project>`), `"../conf" outside src`},
		"duplicate coordinates": {map[string]string{
			"pom.xml":   pomHead + `<modules><module>a</module><module>b</module></modules></project>`,
			"a/pom.xml": `<project><groupId>g</groupId><artifactId>x</artifactId></project>`,
			"b/pom.xml": `<project><groupId>g</groupId><artifactId>x</artifactId></project>`,
		}, "modules a and b are both g:x"},
		"unresolved own property":        {sub(`<project><groupId>${nope}</groupId><artifactId>a</artifactId></project>`), "a/pom.xml: cannot resolve the properties in ${nope}:a"},
		"unresolved dependency property": {sub(`<project><groupId>g</groupId><artifactId>a</artifactId><dependencies><dependency><groupId>g</groupId><artifactId>${x}</artifactId></dependency></dependencies></project>`), "cannot resolve the properties in g:${x}"},
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

		"include build":          {kts(`pluginManagement { includeBuild("build-logic") }`), "settings.gradle.kts: includeBuild: not supported by aval gen java"},
		"project dir":            {kts(`include(":a")` + "\n" + `project(":a").projectDir = file("x")`), "projectDir"},
		"computed include":       {kts(`listOf("a").forEach { include(it) }`), "include: an argument other than a string literal"},
		"concatenated":           {kts(`include(":a" + suffix)`), "include: an argument other than a string literal"},
		"groovy concatenated":    {map[string]string{"settings.gradle": "include ':a' + suffix\n"}, "include: an argument other than a string literal"},
		"interpolated":           {kts(`include(":${name}")`), "a string with interpolation or escapes"},
		"unterminated":           {kts(`include(":a`), "a string with interpolation or escapes"},
		"include outside":        {kts(`include("..")`), `include ":..": ".."`},
		"two root builds":        {kts(`include(":a")`, "build.gradle", "", "build.gradle.kts", ""), "both build.gradle.kts and build.gradle"},
		"two unit builds":        {kts(`include(":a")`, "a/build.gradle", "", "a/build.gradle.kts", ""), "both build.gradle.kts and build.gradle"},
		"accessor in root":       {kts(`include(":a")`, "build.gradle.kts", "dependencies { implementation(projects.a) }"), "build.gradle.kts: type-safe project accessors"},
		"computed project":       {kts(`include(":a")`, "a/build.gradle.kts", "dependencies { implementation(project(name)) }"), "a/build.gradle.kts: project(...): an argument other than a string literal"},
		"apply from kotlin":      {kts(`include(":a")`, "a/build.gradle.kts", `apply(from = "../gradle/shared.gradle.kts")`), "a/build.gradle.kts: apply from: not supported"},
		"apply from groovy":      {kts(`include(":a")`, "build.gradle", "subprojects { apply from: '../x.gradle' }"), "build.gradle: apply from"},
		"source set outside src": {kts(`include(":a")`, "a/build.gradle.kts", `sourceSets { main { java.srcDir("gen") } }`), `a/build.gradle.kts: source or resource directory "gen" outside src`},
		"source set in root":     {kts(`include(":a")`, "build.gradle.kts", `subprojects { sourceSets.main { java.srcDirs("src/a", "") } }`), `"." outside src`},
		"computed source set":    {kts(`include(":a")`, "a/build.gradle.kts", `java.srcDir(layout.buildDirectory.dir("gen"))`), "source set directory: an argument other than a string literal"},
		"ancestor two builds":    {kts(`include(":s:a")`, "s/build.gradle", "", "s/build.gradle.kts", ""), "both build.gradle.kts and build.gradle"},
		"ancestor computed ref":  {kts(`include(":s:a")`, "s/build.gradle.kts", "subprojects { dependencies { implementation(project(name)) } }"), "s/build.gradle.kts: project(...)"},
		"comment hides rest":     {kts("include(\":a\") /* unterminated\ninclude(name)"), ""},
		"comment at the end":     {kts(`include(":a") // no line break after it`), ""},
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
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs file permissions")
	}
	for _, file := range []string{"settings.gradle.kts", "a/build.gradle.kts"} {
		root := tree(t, map[string]string{"settings.gradle.kts": `include(":a")`, "a/build.gradle.kts": ""})
		if err := os.Chmod(filepath.Join(root, filepath.FromSlash(file)), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := (Adapter{}).Units(root); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("%s unreadable: %v", file, err)
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
