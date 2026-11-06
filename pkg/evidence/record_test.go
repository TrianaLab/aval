package evidence

import (
	"encoding/json"
	"testing"
)

func TestRunRecordGolden(t *testing.T) {
	data := readGolden(t, RunRecordVersion)
	var r RunRecord
	if err := strictRoundTrip(data, &r); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRunRecord(data); err != nil {
		t.Fatalf("golden run record rejected: %v", err)
	}
}

// FuzzParseRunRecord: ParseRunRecord never panics, and an accepted record
// survives a JSON round trip.
func FuzzParseRunRecord(f *testing.F) {
	f.Add([]byte(`{"version":"` + RunRecordVersion + `","testId":"t","outcome":"verified","reused":true,"result":"passed"}`))
	f.Add([]byte(`{"version":"` + RunRecordVersion + `","testId":"t","outcome":"hit"}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := ParseRunRecord(data)
		if err != nil {
			return
		}
		b, _ := json.Marshal(r)
		q, err := ParseRunRecord(b)
		if b2, _ := json.Marshal(q); err != nil || string(b2) != string(b) {
			t.Fatalf("round trip: %+v %v vs %+v", q, err, r)
		}
	})
}

func TestParseRunRecord(t *testing.T) {
	ok := `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"error"}`
	if r, err := ParseRunRecord([]byte(ok)); err != nil || r.TestID != "t" || r.Result != "" {
		t.Fatalf("minimal record: %+v %v", r, err)
	}
	for name, doc := range map[string]string{
		"not json":        `{`,
		"unknown version": `{"version":"aval.trianalab.io/run-record/v9","testId":"t","outcome":"miss"}`,
		"no test id":      `{"version":"` + RunRecordVersion + `","outcome":"miss"}`,
		"unknown outcome": `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"hit"}`,
		"unknown result":  `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"miss","result":"flaky"}`,
	} {
		if _, err := ParseRunRecord([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
