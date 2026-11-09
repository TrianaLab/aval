---
"aval": minor
---

`aval init --name NAME --source GLOB... --cacheability CLASS --key NAME|--keyless [--force] -- <command>` writes `aval.yaml` (validated before it is written), with `--key` a signing key pair (`NAME.key` and `NAME.pub`) and `policy.yaml` trusting it at trust level developer, and adds the key and `.aval/` to `.gitignore`. Nothing is inferred: `--source` and `--cacheability` have no default, init lists the files the globs match, rejects a glob that matches nothing and never runs the test. It never replaces a file without `--force` (exit 1); with `--force` it removes the old file and creates a new one, so a symlink is replaced, not written through, and a failed write removes the key pair it wrote. A file name in the preview or an answer is escaped before it reaches the terminal. When a value is missing it asks numbered questions only if stdin and stdout are terminals; otherwise it exits 2 and names the missing flags. The quickstart now starts with `aval init`.
