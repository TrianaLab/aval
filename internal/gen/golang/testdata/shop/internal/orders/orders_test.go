package orders_test

import (
	"testing"

	"example.com/shop/internal/orders"
	"example.com/shop/internal/testutil"
)

func TestTotal(t *testing.T) {
	testutil.Equal(t, orders.Total([]int{100, 50}), testutil.Read(t, "testdata/order.txt"))
}
