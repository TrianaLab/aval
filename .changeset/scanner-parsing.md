---
"aval": patch
---

The mutable-reference scanner reads Dockerfiles the way BuildKit does: a comment line ending in `\` no longer swallows the next instruction, continued lines join with nothing (`FROM db:1\` then `6` is `db:16`), comment and blank lines inside a continuation are dropped, a leading `# escape=` sets the continuation character, and `COPY`/`ADD` heredoc bodies are not read as instructions. A Dockerfile BuildKit refuses (invalid or repeated directive, unterminated heredoc) is a `scan` finding. YAML `image:` keys are matched by their decoded value (`"ima\x67e":`, `? image`, tagged, anchored or alias keys, nested sequences); in a file that is not valid YAML, a quoted key with an undecodable escape is a `scan` finding. `kustomization.yaml` `images:` entries with `newName` or `newTag` and no `digest` are reported. A shell continuation at the very end of a file is no longer dropped.
