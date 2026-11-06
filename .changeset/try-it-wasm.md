---
"aval": patch
---

Try Aval in the browser: the terminal on the home page of the docs site has a "Try it" button that runs the real Aval compiled to WebAssembly, with a simulated test, an in-memory registry and a throwaway key generated in the browser. It supports `aval run` (a miss, then a verified reuse), `aval run --explain`, `aval verify -o json`, `edit <file>`, `tamper` (the next verify fails with exit 5), `reset` and `help`. Nothing is fetched until the click, and without WebAssembly the page keeps the recorded transcript. Keyless signing is not available in the browser.
