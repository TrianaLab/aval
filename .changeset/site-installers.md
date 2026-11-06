---
"aval": patch
---

The installers are served from the docs site: `curl -fsSL https://trianalab.github.io/aval/install.sh | bash` and `irm https://trianalab.github.io/aval/install.ps1 | iex`. The site root redirects to the latest release (to `main` before the first one), and the outdated-version banner shows only on releases older than `latest`, never on `main`.
