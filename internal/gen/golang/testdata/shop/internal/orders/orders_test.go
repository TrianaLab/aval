package orders_test

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/shop/internal/orders"
	"example.com/shop/internal/testutil"
)

// fixtures sits outside the package: aval gen go adds what the tests read.
const fixtures = "../../fixtures"

func TestTotal(t *testing.T) {
	testutil.Equal(t, orders.Total([]int{100, 50}), testutil.Read(t, os.DirFS("testdata"), "order.txt"))
}

func TestFixture(t *testing.T) {
	if _, err := os.ReadFile(filepath.Join(fixtures, "order.json")); err != nil {
		t.Fatal(err)
	}
}
