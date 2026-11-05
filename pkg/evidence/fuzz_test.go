package evidence

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzParse: Parse never panics, and an accepted predicate is valid and
// survives a JSON round trip unchanged in validity.
func FuzzParse(f *testing.F) {
	f.Add([]byte(validPredicate))
	f.Add([]byte(strings.Replace(validPredicate, `"result": "passed"`, `"result": "skipped"`, 1)))
	f.Add([]byte(strings.Replace(validPredicate, `"sha256:`, `"sha512:`, 1)))
	f.Add([]byte(`{"schemaVersion": "v1"}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Parse(data)
		if err != nil {
			return
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("Parse accepted an invalid predicate: %v", err)
		}
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal accepted predicate: %v", err)
		}
		q, err := Parse(b)
		if err != nil {
			t.Fatalf("round trip rejected: %v\n%s", err, b)
		}
		if q.Execution.Fingerprint != p.Execution.Fingerprint || q.Result != p.Result {
			t.Fatalf("round trip changed the claim: %+v vs %+v", p, q)
		}
	})
}
