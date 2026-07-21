#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'release preflight: %s\n' "$*" >&2
  exit 1
}

require_env() {
  local name=$1
  [[ -n "${!name:-}" ]] || die "$name is required"
}

validate_public_keys() {
  local raw=${MINISIGN_PUBLIC_KEYS:-}
  [[ -n "$raw" && "$raw" != "UNSET" ]] || die "MINISIGN_PUBLIC_KEYS is unset"
  [[ "$raw" != "0000000000000000000000000000000000000000000000000000000000000000" ]] || die "legacy placeholder public key is forbidden"

  local keys
  IFS=',' read -r -a keys <<<"$raw"
  (( ${#keys[@]} >= 1 && ${#keys[@]} <= 2 )) || die "configure one key or a two-key rotation overlap"

  local key decoded_hex
  declare -A seen=()
  for key in "${keys[@]}"; do
    [[ -n "$key" && "$key" != *[[:space:]]* ]] || die "public keys must be non-empty base64 payloads without whitespace"
    [[ -z "${seen[$key]:-}" ]] || die "duplicate public key"
    if ! decoded_hex=$(printf '%s' "$key" | base64 --decode 2>/dev/null | od -An -v -tx1 | tr '\n' ' '); then
      die "public key is not valid base64"
    fi
    # A minisign public key is: two algorithm bytes ('Ed'), eight key-ID
    # bytes, and a 32-byte Ed25519 key.
    read -r -a decoded <<<"$decoded_hex"
    (( ${#decoded[@]} == 42 )) || die "public key payload must decode to 42 bytes"
    [[ "${decoded[0]} ${decoded[1]}" == "45 64" ]] || die "public key algorithm must be Ed"
    seen[$key]=1
  done
}

require_env GITHUB_REPOSITORY
require_env GITHUB_REF_TYPE
require_env GITHUB_REF_NAME
require_env GITHUB_SHA
[[ "$GITHUB_REPOSITORY" == "Gentleman-Programming/gentle-ai" ]] || die "unexpected repository $GITHUB_REPOSITORY"
[[ "$GITHUB_REF_TYPE" == "tag" ]] || die "release must run from a tag push"

tag=$GITHUB_REF_NAME
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || die "tag must be exact stable semver (vMAJOR.MINOR.PATCH)"
validate_public_keys

[[ "$(git cat-file -t "refs/tags/$tag")" == "tag" ]] || die "release tag must be annotated"
head_sha=$(git rev-parse 'HEAD^{commit}')
event_sha=$(git rev-parse "$GITHUB_SHA^{commit}")
tag_sha=$(git rev-parse "refs/tags/$tag^{commit}")
[[ "$head_sha" == "$event_sha" && "$head_sha" == "$tag_sha" ]] || die "checkout, event, and tag do not resolve to one commit"

git fetch --no-tags origin '+refs/heads/main:refs/remotes/origin/main'
main_sha=$(git rev-parse 'refs/remotes/origin/main^{commit}')
[[ "$head_sha" == "$main_sha" ]] || die "tagged commit is not exact current origin/main"

remote_tag_sha=$(git ls-remote origin "refs/tags/$tag^{}" | awk 'NR == 1 { print $1 }')
[[ -n "$remote_tag_sha" && "$remote_tag_sha" == "$head_sha" ]] || die "remote annotated tag does not peel to the checkout"

[[ -z "$(git status --porcelain=v1 --untracked-files=all)" ]] || die "release checkout is dirty"
go mod tidy -diff
[[ -z "$(git status --porcelain=v1 --untracked-files=all)" ]] || die "preflight mutated the checkout"

printf 'release preflight: exact tag %s on main %s verified\n' "$tag" "$head_sha"
