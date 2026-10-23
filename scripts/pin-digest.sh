#!/usr/bin/env bash
# Resolves the immutable digest of a pushed collector image and prints the
# manifest artifact line to pin. With --write it also rewrites the artifact
# image in manifest.yaml.
#
# Usage: pin-digest.sh <repo:tag> [--write]
#
# The manifest is embedded in the image it describes, so a release cannot pin
# the digest of the image that carries it. Pin the digest after the push and
# commit the manifest change in the next release PR; see README.md "Release".
set -euo pipefail
ref="${1:?usage: pin-digest.sh <repo:tag> [--write]}"
mode="${2:-}"
cd "$(dirname "$0")/.."
digest="$(docker inspect --format='{{index .RepoDigests 0}}' "$ref")"
case "$digest" in
  ghcr.io/eshu-hq/eshu-collector-pagerduty@sha256:????????????????????????????????????????????????????????????????) ;;
  *) echo "unexpected digest reference: $digest" >&2; exit 1 ;;
esac
echo "Pin this in manifest.yaml artifacts:"
echo "  image: ${digest}"
if [ "$mode" = "--write" ]; then
  tmp="$(mktemp)"
  trap 'rm -f "$tmp"' EXIT
  sed -E "s#^(      image: )ghcr\.io/eshu-hq/eshu-collector-pagerduty@sha256:[0-9A-Fa-f]{64}\$#\1${digest}#" manifest.yaml > "$tmp"
  cat "$tmp" > manifest.yaml
  echo "manifest.yaml updated"
fi
