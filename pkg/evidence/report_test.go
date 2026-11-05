package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

// Golden files freeze the JSON shape of each format version (AGENTS.md
// invariant 10, docs/artifact-format.md "Versioning and compatibility").
// They are append-only: a file is never edited or deleted, a new version
// adds a new file. Each sets every field, so dropping, renaming or retyping
// one fails here, while a new omitempty field (additive) passes.
var goldens = map[string]string{
	PredicateType: "testdata/statement-execution-v2.golden.json",
	ReportVersion: "testdata/verify-report-v1.golden.json",
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
