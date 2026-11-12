// Command shop prints the total of an order.
package main

import (
	"fmt"

	"example.com/shop/internal/orders"
)

func main() { fmt.Println(orders.Total([]int{1, 2})) }
