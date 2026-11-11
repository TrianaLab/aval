---
"aval": patch
---

The pinning scanner no longer reports `npx --no`, `npx --no-install`,
`npm exec --no`, `pnpm exec` and `yarn exec`: they run only what is already
installed. Plain `npx`, `npm exec`, `pnpm dlx`, `yarn dlx` and `bunx` are still
reported, as are packages named by `-p`, and the fix text now mentions
`npx --no <bin>`.
