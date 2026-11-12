package dotnet

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/gen"
)

var update = flag.Bool("update", false, "rewrite testdata/golden.yaml")

const fixture = "testdata/shop"

// generate runs the adapter on root and rewrites testdata/input.yaml.
func generate(t *testing.T, root string) string {
	t.Helper()
	units, err := Adapter{}.Units(root)
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.ReadFile("testdata/input.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out, err := gen.Rewrite(in, "dotnet", units, nil)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestGolden(t *testing.T) {
	got := generate(t, fixture)
	if *update {
		if err := os.WriteFile("testdata/golden.yaml", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("output differs from testdata/golden.yaml (go test -update):\n%s", gen.Diff("golden.yaml", "generated", want, []byte(got)))
	}
}

// files reads the fixture into a map, path to contents.
func files(t *testing.T) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := fs.WalkDir(os.DirFS(fixture), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, rerr := os.ReadFile(filepath.Join(fixture, p))
			m[p] = string(data)
			err = rerr
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// digest fingerprints the test name of set over files.
func digest(t *testing.T, set string, files map[string]string, name string) string {
	t.Helper()
	cs, err := contract.ParseAll([]byte(set))
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for p, data := range files {
		fsys[p] = &fstest.MapFile{Data: []byte(data)}
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

// A change to any file a test's project builds from, directly or through a
// project it references, changes that test's fingerprint; a change to an
// unrelated project does not.
func TestFalseHit(t *testing.T) {
	set := generate(t, fixture)
	base := files(t)
	const api = "dotnet.shop.api.tests"
	before := digest(t, set, base, api)
	for _, f := range []string{
		"src/Shop.Core/Order.cs",                     // transitive: Api.Tests -> Api -> Data -> Core
		"src/Shop.Data/Shop.Data.csproj",             // a referenced project file
		"tools/Shop.Legacy/Legacy.vb",                // referenced, not in the solution
		"shared/Version.cs",                          // Compile Include outside the project
		"protos/orders.proto",                        // a wildcard item outside the project
		"Directory.Build.props",                      // implicit import at the root
		"tests/Directory.Build.props",                // implicit import above the project
		"Directory.Packages.props",                   // central package versions
		"tests/Shop.Api.Tests/Shop.Api.Tests.fsproj", // the project itself
	} {
		changed := maps(base)
		changed[f] += "// changed\n"
		if digest(t, set, changed, api) == before {
			t.Errorf("%s reused after %s changed", api, f)
		}
	}
	for _, f := range []string{"tests/Shop.Core.Tests/OrderTests.cs", "samples/Shop.Sample/Shop.Sample.csproj"} {
		changed := maps(base)
		changed[f] += "// changed\n"
		if digest(t, set, changed, api) != before {
			t.Errorf("%s changed after the unrelated %s changed", api, f)
		}
	}
	core := digest(t, set, base, "dotnet.shop.core.tests")
	changed := maps(base)
	changed["src/Shop.Api/Program.cs"] += "// changed\n"
	if digest(t, set, changed, "dotnet.shop.core.tests") != core {
		t.Error("dotnet.shop.core.tests changed after Shop.Api, which it does not reference, changed")
	}
}

func maps(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// A new ProjectReference is drift: the generated file differs and the diff
// names the new dependency.
func TestNewReferenceIsDrift(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	golden := generate(t, root)
	proj := filepath.Join(root, "tests", "Shop.Core.Tests", "Shop.Core.Tests.csproj")
	data, _ := os.ReadFile(proj)
	data = []byte(strings.Replace(string(data), "</ItemGroup>", `  <ProjectReference Include="..\..\src\Shop.Data\Shop.Data.csproj" />`+"\n  </ItemGroup>", 1))
	if err := os.WriteFile(proj, data, 0o644); err != nil {
		t.Fatal(err)
	}
	now := generate(t, root)
	if d := gen.Diff("aval.yaml", "generated", []byte(golden), []byte(now)); !strings.Contains(d, "+      - dotnet.shop.data") {
		t.Errorf("the new reference is not drift:\n%s", d)
	}
}

// write creates files under a new directory and returns it.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, data := range files {
		p = filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const sdk = `<Project Sdk="Microsoft.NET.Sdk"/>`

func ref(p string) string {
	return `<Project Sdk="Microsoft.NET.Sdk"><ItemGroup><ProjectReference Include="` + p + `"/></ItemGroup></Project>`
}

// Without a solution every project file under the root is a unit, except
// under hidden, bin, obj and node_modules directories.
func TestWalk(t *testing.T) {
	root := write(t, map[string]string{
		"App.csproj":                  ref(`lib\Lib.fsproj`),
		"lib/Lib.fsproj":              sdk,
		"lib/bin/Debug/Copy.csproj":   sdk,
		"lib/obj/Gen.csproj":          sdk,
		".git/x/Hidden.csproj":        sdk,
		"web/node_modules/m/M.vbproj": sdk,
		"lib/notes.txt":               "not a project",
	})
	units, err := Adapter{}.Units(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []gen.Unit{
		{ID: "App", Source: []string{"**"}, Deps: []string{"Lib"}, Command: []string{"dotnet", "test", "App.csproj"}},
		{ID: "Lib", Source: []string{"lib/**"}, Command: []string{"dotnet", "test", "lib/Lib.fsproj"}},
	}
	if !equal(units, want) {
		t.Errorf("units %+v, want %+v", units, want)
	}
}

func equal(a, b []gen.Unit) bool {
	return slices.EqualFunc(a, b, func(x, y gen.Unit) bool {
		return x.ID == y.ID && slices.Equal(x.Source, y.Source) && slices.Equal(x.Deps, y.Deps) && slices.Equal(x.Command, y.Command)
	})
}

// A .slnx lists projects by Path; solution folders are skipped. Imports
// are followed: a ProjectReference or item in an imported file counts, and
// a file outside the project joins its scope.
func TestSlnxAndImports(t *testing.T) {
	root := write(t, map[string]string{
		"All.slnx": `<Solution><Folder Name="/src/"><Project Path="src\App\App.csproj"/></Folder><Folder Name="/docs/"><File Path="README.md"/></Folder></Solution>`,
		"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <Import Project="Sdk.props" Sdk="Microsoft.NET.Sdk"/>
  <Import Project="$(MSBuildExtensionsPath)\x.targets"/>
  <Import Project="$(MSBuildThisFileDirectory)..\..\build\common.props"/>
  <Import Project="local.props" Condition="Exists('local.props')"/>
  <Import Project="local.props"/>
  <ItemGroup><Reference Include="Old"><HintPath>..\..\libs\Old.dll</HintPath></Reference></ItemGroup>
  <ItemGroup><None Include="$(MSBuildProjectDirectory)\..\..\assets\logo.png"/><Content Include="a.txt;;b.txt"/></ItemGroup>
</Project>`,
		"src/App/local.props": `<Project/>`,
		"build/common.props":  `<Project><Import Project="common.props"/><ItemGroup><ProjectReference Include="$(MSBuildThisFileDirectory)..\src\Lib\Lib.csproj"/><Compile Include="..\..\shared\**\*.cs"/></ItemGroup></Project>`,
		"src/Lib/Lib.csproj":  sdk,
	})
	units, err := Adapter{}.Units(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []gen.Unit{
		{ID: "App", Source: []string{"src/App/**", "build/common.props", "shared/**/*.cs", "libs/Old.dll", "assets/logo.png"}, Deps: []string{"Lib"}, Command: []string{"dotnet", "test", "src/App/App.csproj"}},
		{ID: "Lib", Source: []string{"src/Lib/**"}, Command: []string{"dotnet", "test", "src/Lib/Lib.csproj"}},
	}
	if !equal(units, want) {
		t.Errorf("units %+v, want %+v", units, want)
	}
}

// Fail closed: anything the adapter cannot read or resolve is an error and
// no units are returned.
func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"malformed project", map[string]string{"A.csproj": "<Project>"}, "A.csproj: XML syntax error"},
		{"malformed slnx", map[string]string{"A.slnx": "<Solution>"}, "A.slnx: XML syntax error"},
		{"missing listed project", map[string]string{"A.sln": `Project("{X}") = "B", "B\B.csproj", "{Y}"` + "\n"}, "B.csproj"},
		{"missing reference", map[string]string{"A.csproj": ref(`..\B\B.csproj`)}, "outside the repository"},
		{"missing local reference", map[string]string{"A.csproj": ref(`B\B.csproj`)}, "B/B.csproj"},
		{"reference to a non-project", map[string]string{"A.csproj": ref(`B\B.dll`)}, "not a .csproj"},
		{"absolute reference", map[string]string{"A.csproj": ref(`C:\B\B.csproj`)}, "outside the repository"},
		{"property in a reference", map[string]string{"A.csproj": ref(`$(Root)\B.csproj`)}, "MSBuild properties"},
		{"property in an item", map[string]string{"A.csproj": `<Project><ItemGroup><Compile Include="$(Shared)\x.cs"/></ItemGroup></Project>`}, "MSBuild properties"},
		{"missing import", map[string]string{"A.csproj": `<Project><Import Project="..\x.props"/></Project>`}, "outside the repository"},
		{"missing local import", map[string]string{"A.csproj": `<Project><Import Project="x.props"/></Project>`}, "x.props"},
		{"malformed import", map[string]string{"A.csproj": `<Project><Import Project="x.props"/></Project>`, "x.props": "<"}, "x.props: XML syntax error"},
		{"malformed Directory.Build.props", map[string]string{"p/A.csproj": sdk, "Directory.Build.props": "<"}, "Directory.Build.props: XML syntax error"},
		{"bracket in an item", map[string]string{"p/A.csproj": `<Project><ItemGroup><None Include="..\[x]\y"/></ItemGroup></Project>`}, "glob characters"},
		{"wildcard in a hint path", map[string]string{"p/A.csproj": `<Project><ItemGroup><Reference Include="x"><HintPath>..\l\*.dll</HintPath></Reference></ItemGroup></Project>`}, "glob characters"},
		{"glob characters in a directory", map[string]string{"[p]/A.csproj": sdk}, "glob characters"},
		{"listed project outside", map[string]string{"A.sln": `Project("{X}") = "B", "..\B\B.csproj", "{Y}"` + "\n"}, "A.sln: \"../B/B.csproj\": outside the repository"},
		{"glob characters in an import", map[string]string{"p/A.csproj": `<Project><Import Project="..\[b]\x.props"/></Project>`, "[b]/x.props": "<Project/>"}, "glob characters"},
		{"file above that is not Directory.*", map[string]string{"A.csproj": `<Project><Import Project="$([MSBuild]::GetPathOfFileAbove('Custom.props', '$(MSBuildThisFileDirectory)../'))"/></Project>`}, "GetPathOfFileAbove of a file other than"},
		{"file above from a sibling directory", map[string]string{"p/A.csproj": `<Project><Import Project="$([MSBuild]::GetPathOfFileAbove('Directory.Build.props', '$(MSBuildThisFileDirectory)../build/'))"/></Project>`}, "GetPathOfFileAbove from build"},
		{"file above with a suffix", map[string]string{"p/A.csproj": `<Project><Import Project="$([MSBuild]::GetPathOfFileAbove('Directory.Build.props', '$(MSBuildThisFileDirectory)'))\..\build\extra.props"/></Project>`, "build/extra.props": "<Project/>"}, "MSBuild properties"},
		{"file above with a prefix", map[string]string{"p/A.csproj": `<Project><Import Project="$(Root)$([MSBuild]::GetPathOfFileAbove('Directory.Build.props'))"/></Project>`}, "MSBuild properties"},
		{"file above from a property", map[string]string{"A.csproj": `<Project><Import Project="$([MSBuild]::GetPathOfFileAbove('Directory.Build.props', '$(Dir)'))"/></Project>`}, "MSBuild properties"},
		{"wildcard import", map[string]string{"p/A.csproj": `<Project><Import Project="..\b\*.targets" Condition="true"/></Project>`, "b/x.targets": "<Project/>"}, "wildcard imports"},
		{"two solutions", map[string]string{"A.sln": "", "B.slnx": "<Solution/>"}, "several solutions"},
		{"same project name", map[string]string{"a/X.csproj": sdk, "b/X.fsproj": sdk}, "two projects named X"},
		{"invalid glob", map[string]string{"p/A.csproj": `<Project><ItemGroup><None Include="..\x**"/></ItemGroup></Project>`}, "whole path segment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			units, err := Adapter{}.Units(write(t, tc.files))
			if err == nil || !strings.Contains(err.Error(), tc.want) || units != nil {
				t.Errorf("units %v, error %v; want %q", units, err, tc.want)
			}
		})
	}
	if _, err := units(fstest.MapFS{`a\b/A.csproj`: {Data: []byte(sdk)}}); err == nil || !strings.Contains(err.Error(), "slash-separated") {
		t.Errorf("a backslash in a directory name: %v", err)
	}
	if _, err := (Adapter{}).Units(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("a missing root gave no error")
	}
	if runtime.GOOS != "windows" {
		root := write(t, map[string]string{"A.csproj": sdk, "A.sln": `Project("{X}") = "A", "A.csproj", "{Y}"`})
		_ = os.Chmod(filepath.Join(root, "A.sln"), 0)
		_ = os.Chmod(filepath.Join(root, "A.csproj"), 0)
		if _, err := (Adapter{}).Units(root); err == nil {
			t.Error("an unreadable solution gave no error")
		}
		_ = os.Remove(filepath.Join(root, "A.sln"))
		if _, err := (Adapter{}).Units(root); err == nil {
			t.Error("an unreadable project gave no error")
		}
		sub := write(t, map[string]string{"d/A.csproj": sdk})
		_ = os.Chmod(filepath.Join(sub, "d"), 0)
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(sub, "d"), 0o755) })
		if _, err := (Adapter{}).Units(sub); err == nil {
			t.Error("an unreadable directory gave no error")
		}
	}
}

// FuzzProject: any project file either fails or gives units whose globs are
// valid and stay inside the root; nothing panics.
func FuzzProject(f *testing.F) {
	f.Add(ref(`..\B\B.csproj`))
	f.Add(`<Project><Import Project="x.props"/><ItemGroup><Compile Include="..\s\*.cs;a.cs"/><Reference Include="r"><HintPath>..\l\r.dll</HintPath></Reference></ItemGroup></Project>`)
	f.Add(`<Project><ItemGroup><ProjectReference Include="$(MSBuildProjectDirectory)\..\B\B.csproj"/></ItemGroup></Project>`)
	f.Fuzz(func(t *testing.T, project string) {
		units, err := units(fstest.MapFS{
			"p/A.csproj": {Data: []byte(project)},
			"B/B.csproj": {Data: []byte(sdk)},
			"p/x.props":  {Data: []byte("<Project/>")},
		})
		if err != nil {
			return
		}
		for _, u := range units {
			for _, g := range u.Source {
				if err := contract.ValidateGlob(g); err != nil {
					t.Fatalf("unit %s: %v", u.ID, err)
				}
			}
		}
	})
}
