---
"aval": patch
---

`aval stats` finds run records on registries that do not sort tags, such as GHCR: it lists every tag and filters by the `stats-` prefix instead of starting the listing after `stats`. Up to 64 pages of tags are read (6400 tags at a registry page size of 100); past that the report carries a truncation warning.
