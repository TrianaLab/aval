// Command monorepo prints a greeting and the version.
package main

import (
	"fmt"

	"example.com/monorepo/internal/greet"
	"example.com/monorepo/pkg/version"
)

func main() {
	fmt.Println(greet.Hello("world"), version.String())
}
