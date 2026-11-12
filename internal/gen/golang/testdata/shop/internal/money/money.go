// Package money formats amounts.
package money

import "strconv"

// Format returns cents as a decimal string.
func Format(cents int) string { return strconv.Itoa(cents/100) + "." + strconv.Itoa(cents%100) }
