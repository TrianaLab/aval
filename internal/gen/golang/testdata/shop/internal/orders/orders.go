// Package orders sums orders.
package orders

import (
	"os"

	"example.com/shop/internal/money"
)

// Total returns the formatted sum of items.
func Total(items []int) string {
	sum := 0
	for _, c := range items {
		sum += c
	}
	return money.Format(sum)
}

// Rates reads the rate table. Non-test code: every dependent's test runs
// it, so aval gen go puts the file in the fragment they all use.
func Rates() ([]byte, error) { return os.ReadFile("../../fixtures/rates.json") }
