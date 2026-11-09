---
"aval": patch
---

On a terminal, Aval shows a spinner on stderr when looking up and fetching a result, pushing one (`aval run --push`, `aval push`) or reading run records (`aval stats`) takes more than 300ms, for example `⠹ looking up localhost:5001… 4s`. It runs only in styled mode with stderr a terminal, is erased before the decision or the test's output prints, erases itself before a warning, and leaves the terminal clean on Ctrl-C. Windows gets no spinner. Plain output, exit codes and `aval verify` are unchanged.
