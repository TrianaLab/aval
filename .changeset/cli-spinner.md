---
"aval": patch
---

On a terminal, `aval run`, `aval push` and `aval stats` show a spinner on stderr while they wait on the registry for more than 300ms, for example `⠹ looking up localhost:5001… 4s`. It runs only in styled mode with stderr a terminal, is erased before the decision or the test's output prints, erases itself before a warning, and leaves the terminal clean on Ctrl-C. Plain output, exit codes and `aval verify` are unchanged.
