//go:build integration

package web

import (
	"testing"

	"example.com/shop/internal/money"
)

func TestPrice(t *testing.T) { _ = money.Format(1) }
