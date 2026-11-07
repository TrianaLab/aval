---
"aval": patch
---

Docs site: live run statistics. The home metric strip and a new Stats page (Project > Stats) read `stats.json`, which the docs workflow rebuilds twice an hour and on every push from `aval stats --by repository --since 30d` over the public stats registry `ghcr.io/trianalab/aval-stats`, without rebuilding any page; a failed refresh keeps the previous file, and a missing or stale file shows a neutral state. Aval's own CI now publishes its run records there. The Stats page explains how to keep statistics in your own registry and how a repository joins the public aggregate.
