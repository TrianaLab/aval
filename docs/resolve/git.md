# Git references

`git: <url>@<ref>` asks the remote, with `git ls-remote`, which commit a
branch or tag points to, and hashes the commit id.

```yaml
scope:
  resolve:
    - git: https://github.com/acme/fixtures@main
    - git: https://github.com/acme/fixtures@v1.4.0   # an annotated tag: its commit
    - git: ssh://git@github.com/acme/private@refs/heads/release
```

- The ref is a branch, a tag, `HEAD` or a full ref name. A name that is
  both a branch and a tag fails: write `refs/heads/<name>` or
  `refs/tags/<name>`. A full commit id resolves to itself, after
  `git ls-remote` shows no branch or tag with that exact name; if one
  exists, it fails.
- URLs: `https`, `http`, `ssh`, `git` or `file`, with a scheme
  (`ssh://git@host/repo`, not `git@host:repo`).
- Needs `git` on the `PATH`. Credentials come from git itself: credential
  helpers, SSH keys. Git never prompts.

A `git clone` or `git pull` the [scanner](../pinning.md) reports still
blocks reuse with a `git` entry: `scope.resolve` never clears a finding.
Pin the clone by commit, or accept it with `scope.allowMutable`. The `git`
entry hashes the commit, so a moved branch changes the fingerprint.

**Pin instead** with a commit: `git clone URL && git -C dir checkout
<40-hex>`, or a submodule. Resolve is for a branch you mean to follow, such
as shared test fixtures.
