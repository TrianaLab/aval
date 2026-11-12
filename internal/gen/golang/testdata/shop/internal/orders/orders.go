// Package orders sums orders.
package orders

import "example.com/shop/internal/money"

// Total returns the formatted sum of items.
func Total(items []int) string {
	sum := 0
	for _, c := range items {
		sum += c
	}
	return money.Format(sum)
}
