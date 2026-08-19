#!/bin/sh
# Refuse to cut a release the changelog does not describe.
#
# A changelog only stays true if writing it is part of releasing. This runs from
# `make release`, reads the tag being released, and fails when CHANGELOG.md has
# no section for it -- so the entry is written before the artifacts exist, not
# remembered afterwards.
set -eu

tag=$(git describe --exact-match --tags 2>/dev/null || true)
if [ -z "$tag" ]; then
	echo "changelog: HEAD is not tagged, nothing to check" >&2
	exit 0
fi

if ! grep -q "^## ${tag} " CHANGELOG.md; then
	echo "changelog: CHANGELOG.md has no '## ${tag} (date)' section." >&2
	echo "changelog: write the entry for ${tag} before releasing it." >&2
	exit 1
fi
