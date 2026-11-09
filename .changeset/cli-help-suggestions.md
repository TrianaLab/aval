---
"aval": minor
---

`aval --help` groups commands into Core, Publishing and Statistics, and every command has an example. A mistyped command gets a suggestion (`aval rn` and `aval help rn` print `unknown command "rn"; did you mean "run"?` and exit 2; `aval help rn` used to print the root help and exit 0). A mistyped flag gets the closest flag of that command and a pointer to its help (`unknown flag: --registy; did you mean --registry? see aval run --help`). `aval run --help` is shorter. Exit codes are unchanged.
