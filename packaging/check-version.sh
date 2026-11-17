#!/usr/bin/env bash
# Usage: check-version.sh <tag>  (e.g. v0.20.0 or v0.20.0-rc.1)
# Fails unless the tag is the version in the workspace Cargo.toml, prefixed with v.
set -euo pipefail
tag="${1:?usage: check-version.sh <tag>}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
version="$(sed -n '/^\[workspace.package\]/,/^\[/{s/^version = "\(.*\)"$/\1/p}' "$root/Cargo.toml")"
if [[ "$tag" != "v$version" ]]; then
  echo "tag $tag does not match the Cargo version $version (expected v$version)" >&2
  exit 1
fi
if [[ "$version" == *-* && ! "$version" =~ -(alpha|beta|rc)\.[0-9]+$ ]]; then
  echo "pre-release label in $version must be -alpha.N, -beta.N or -rc.N" >&2
  exit 1
fi
echo "$version"
