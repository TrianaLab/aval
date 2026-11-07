---
"aval": patch
---

Try Aval in the browser: click the terminal on the home page of the docs site (or its "Try it live" button) and it runs the real Aval compiled to WebAssembly, with a simulated test, an in-memory registry and a throwaway key generated in the browser. It supports `aval run` (a miss, then a verified reuse), `aval run --explain`, `aval verify -o json`, `edit <file>`, `tamper` (the next verify fails with exit 5), `reset` and `help`. Nothing is fetched until the visitor clicks the terminal or presses a key in it, and without WebAssembly the page keeps the recorded transcript. Keyless signing is not available in the browser.
