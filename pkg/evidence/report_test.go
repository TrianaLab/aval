package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Golden files freeze the JSON shape of each format version (AGENTS.md
// invariant 10, docs/artifact-format.md "Versioning and compatibility").
// They only grow: an existing field or value is never removed or changed, an
// additive change adds its new field to the golden file of its version, and
// a new version adds a new file next to the old ones. Each sets every field
// (TestGoldensSetEveryField), so dropping, renaming or retyping one fails
// here, and so does a new field missing from its golden file.
var goldens = map[string]string{
	PredicateType:    "testdata/statement-execution-v2.golden.json",
	ReportVersion:    "testdata/verify-report-v1.golden.json",
	RunRecordVersion: "testdata/run-record-v1.golden.json",
}

// strictRoundTrip decodes data into v rejecting unknown fields, encodes v
// again and reports an error unless the result is the same JSON document.
func strictRoundTrip(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("golden document no longer decodes: %w", err)
	}
	got, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var want, back any
	if err := json.Unmarshal(data, &want); err != nil {
		return err
	}
	if err := json.Unmarshal(got, &back); err != nil {
		return err
	}
	if !reflect.DeepEqual(want, back) {
		return fmt.Errorf("JSON shape changed:\n got %s\nwant %s", got, data)
	}
	return nil
}

func readGolden(t *testing.T, version string) []byte {
	t.Helper()
	file, ok := goldens[version]
	if !ok {
		t.Fatalf("no golden file for %s: add one for the new version and keep the old ones", version)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestStatementGolden(t *testing.T) {
	data := readGolden(t, PredicateType)
	var st Statement
	if err := strictRoundTrip(data, &st); err != nil {
		t.Fatal(err)
	}
	if st.Type != StatementType || st.PredicateType != PredicateType {
		t.Fatalf("statement identifiers: %q %q", st.Type, st.PredicateType)
	}
	var p Predicate
	if err := strictRoundTrip(st.Predicate, &p); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(st.Predicate); err != nil {
		t.Fatalf("golden predicate rejected: %v", err)
	}
}

func TestReportGolden(t *testing.T) {
	data := readGolden(t, ReportVersion)
	var r Report
	if err := strictRoundTrip(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.Version != ReportVersion || r.Predicate == nil || r.Predicate.Validate() != nil {
		t.Fatalf("golden report: version %q, predicate %+v", r.Version, r.Predicate)
	}
	// Fields documented as always present must not become omitempty.
	b, err := json.Marshal(Report{})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "outcome", "testId", "fingerprint"} {
		if !bytes.Contains(b, []byte(`"`+k+`"`)) {
			t.Errorf("%s must always be present", k)
		}
	}
}

// Every JSON field of the frozen types is set in its golden file, so a new
// field is guarded from the change that adds it.
func TestGoldensSetEveryField(t *testing.T) {
	var st, r, rec map[string]any
	if err := json.Unmarshal(readGolden(t, PredicateType), &st); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readGolden(t, ReportVersion), &r); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readGolden(t, RunRecordVersion), &rec); err != nil {
		t.Fatal(err)
	}
	for _, m := range [][]string{
		missingFields("statement", reflect.TypeOf(Statement{}), st),
		missingFields("predicate", reflect.TypeOf(Predicate{}), st["predicate"]),
		missingFields("report", reflect.TypeOf(Report{}), r),
		missingFields("run record", reflect.TypeOf(RunRecord{}), rec),
	} {
		for _, f := range m {
			t.Errorf("%s: not set in the golden file", f)
		}
	}
}

// missingFields lists the JSON fields of typ, recursively, that the decoded
// document v does not set. Slices are checked through their first element.
func missingFields(path string, typ reflect.Type, v any) []string {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
		typ = typ.Elem()
		if s, ok := v.([]any); ok && len(s) > 0 {
			v = s[0]
		}
	}
	if typ.Kind() != reflect.Struct || typ == reflect.TypeOf(time.Time{}) {
		return nil
	}
	m, _ := v.(map[string]any)
	var missing []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		fv, ok := m[name]
		if !ok {
			missing = append(missing, path+"."+name)
			continue
		}
		missing = append(missing, missingFields(path+"."+name, f.Type, fv)...)
	}
	return missing
}

func TestMissingFieldsCatchesNewField(t *testing.T) {
	type inner struct {
		A string `json:"a"`
		B string `json:"b,omitempty"`
	}
	type doc struct {
		Items []*inner `json:"items"`
		Extra string   `json:"extra,omitempty"`
		Skip  string   `json:"-"`
		Plain string
		Bare  string
		low   string
	}
	var v any
	if err := json.Unmarshal([]byte(`{"items":[{"a":"x"}],"Plain":"p"}`), &v); err != nil {
		t.Fatal(err)
	}
	got := missingFields("doc", reflect.TypeOf(doc{low: ""}), v)
	want := []string{"doc.items.b", "doc.extra", "doc.Bare"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The golden check must catch a renamed, dropped, retyped or newly
// required field.
func TestGoldenCatchesShapeChange(t *testing.T) {
	type current struct {
		Version string `json:"version"`
		Outcome string `json:"verdict"`
		Count   int    `json:"count"`
	}
	for name, doc := range map[string]string{
		"renamed field":  `{"version":"x","outcome":"miss","count":1}`,
		"dropped field":  `{"version":"x","verdict":"miss","count":1,"testId":"t"}`,
		"retyped field":  `{"version":"x","verdict":"miss","count":"1"}`,
		"required field": `{"version":"x","verdict":"miss"}`,
		"not json":       `{`,
	} {
		if err := strictRoundTrip([]byte(doc), &current{}); err == nil {
			t.Errorf("%s: not reported", name)
		}
	}
	if err := strictRoundTrip([]byte(`{"version":"x","verdict":"miss","count":1}`), &current{}); err != nil {
		t.Errorf("unchanged shape reported: %v", err)
	}
}

// A report without rejected candidates keeps its pre-existing shape, and an
// older document without the field still parses.
func TestReportWithoutRejected(t *testing.T) {
	old := `{"version":"` + ReportVersion + `","outcome":"miss","testId":"t","fingerprint":"sha256:aa","reason":"no attestation"}`
	var r Report
	if err := json.Unmarshal([]byte(old), &r); err != nil || r.Rejected != nil {
		t.Fatalf("%+v %v", r, err)
	}
	got, err := json.Marshal(r)
	if err != nil || string(got) != old {
		t.Fatalf("got %s %v", got, err)
	}
}
