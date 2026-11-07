//go:build js && wasm

// Command aval-wasm is the docs site's "Try it" terminal: internal/demo
// compiled to WebAssembly (`make docs-wasm`). It defines avalDemo(line),
// which runs one command line and returns {output, code}, then calls
// onAvalDemoReady if the page defined it.
package main

import (
	"strings"
	"syscall/js"

	"github.com/TrianaLab/aval/internal/demo"
)

func main() {
	d := demo.New()
	js.Global().Set("avalDemo", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 1 || args[0].Type() != js.TypeString {
			return map[string]any{"output": "", "code": 2}
		}
		var b strings.Builder
		code := d.Exec(args[0].String(), &b, &b)
		return map[string]any{"output": b.String(), "code": code}
	}))
	if ready := js.Global().Get("onAvalDemoReady"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}
	select {} // keep avalDemo callable
}
