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
		// Inconsistent records (#88): a hand-edited record must not publish
		// a reuse that never happened.
		"reused on a miss":            `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"miss","reused":true}`,
		"reused on an error":          `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"error","reused":true}`,
		"reused on verif. failure":    `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"verification-failed","reused":true}`,
		"reused but the test ran":     `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"verified","reused":true,"testDurationMs":5}`,
		"reused, negative duration":   `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"verified","reused":true,"testDurationMs":-1}`,
		"unknown verified result":     `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"verified","verifiedResult":"flaky"}`,
		"verified result on a miss":   `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"miss","verifiedResult":"passed"}`,
		"verified result on an error": `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"error","verifiedResult":"failed"}`,
	} {
		if _, err := ParseRunRecord([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Consistent records Aval writes still parse: a reuse, a shadow run that ran
// the test, and a pre-#88 record without verifiedResult.
func TestParseRunRecordConsistent(t *testing.T) {
	for name, doc := range map[string]string{
		"reuse":         `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"verified","reused":true,"result":"passed","verifiedResult":"passed","savedDurationMs":9}`,
		"shadow":        `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"verified","result":"failed","verifiedResult":"passed","testDurationMs":9}`,
		"pre-88 reuse":  `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"verified","reused":true,"result":"passed"}`,
		"miss that ran": `{"version":"` + RunRecordVersion + `","testId":"t","outcome":"miss","result":"passed","testDurationMs":9}`,
	} {
		if _, err := ParseRunRecord([]byte(doc)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestStatsReportGolden(t *testing.T) {
	data := readGolden(t, StatsReportVersion)
	var r StatsReport
	if err := strictRoundTrip(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.Version != StatsReportVersion {
		t.Fatalf("golden stats report: version %q", r.Version)
	}
}
