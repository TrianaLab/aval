//go:build integration

package counter

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Needs the redis service from compose.yaml. Skips when REDIS_ADDR is unset,
// so `go test -tags=integration ./...` without docker passes. Once REDIS_ADDR
// is set (aval.yaml sets it) an unreachable service is a failure, not a skip:
// a skipped test that reports "pass" must never be attested and reused.
func TestIntegrationIncr(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR not set")
	}
	c, err := Dial(addr)
	if err != nil {
		t.Fatalf("redis unreachable at %s: %v", addr, err)
	}
	defer c.Close()

	key := fmt.Sprintf("aval-example-%d", time.Now().UnixNano())
	for want := int64(1); want <= 3; want++ {
		n, err := c.Incr(key)
		if err != nil || n != want {
			t.Fatalf("Incr = %d, %v; want %d", n, err, want)
		}
	}
	if v, ok, err := c.Get(key); err != nil || !ok || v != "3" {
		t.Fatalf("Get = %q, %v, %v; want \"3\"", v, ok, err)
	}
}
