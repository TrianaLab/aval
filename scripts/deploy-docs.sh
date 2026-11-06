#!/usr/bin/env bash
# Publishes the docs site to the gh-pages branch (.github/workflows/docs.yaml).
#
#   scripts/deploy-docs.sh branch main     # version `main`
#   scripts/deploy-docs.sh tag v0.3.0      # version 0.3.0 with the `latest` alias
#
# Then the site root redirects to `latest` when a release exists, else to
# `main`, and the installers are copied to the gh-pages root, so
# <site>/install.sh and <site>/install.ps1 always serve the scripts of the
# commit just deployed. PUSH=true pushes gh-pages to origin; without it the
# commits stay local (scripts/test-deploy-docs.sh runs it both ways).
# Needs mike and the packages in docs/requirements.txt.
set -euo pipefail

ref_type=${1:?usage: deploy-docs.sh branch|tag <ref name>}
ref_name=${2:?usage: deploy-docs.sh branch|tag <ref name>}
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
export NO_MKDOCS_2_WARNING=1

git fetch origin gh-pages:gh-pages 2>/dev/null || echo "no gh-pages branch on origin yet; mike creates it"

if [ "$ref_type" = "tag" ]; then
  mike deploy --update-aliases "${ref_name#v}" latest
else
  mike deploy main
fi

if mike list latest >/dev/null 2>&1; then
  mike set-default latest
else
  mike set-default main # before the first release
fi

pages=$(mktemp -d)
git worktree add -q "$pages" gh-pages
cp scripts/get-aval.sh "$pages/install.sh"
cp scripts/install.ps1 "$pages/install.ps1"
git -C "$pages" add install.sh install.ps1
git -C "$pages" diff --cached --quiet ||
  git -C "$pages" commit -q -m "Publish the installers from $(git rev-parse --short HEAD)"
git worktree remove "$pages"

if [ "${PUSH:-false}" = "true" ]; then
  git push origin gh-pages
fi
