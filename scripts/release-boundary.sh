#!/usr/bin/env bash
# Validate immutable release source before granting permission to allocate a tag.
set -euo pipefail
[[ "$RELEASE_TAG" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
[[ "$(git rev-parse HEAD)" == "$(git rev-parse "$EVENT_SHA^{commit}")" ]]
git merge-base --is-ancestor HEAD "refs/remotes/origin/$DEFAULT_BRANCH"
create_tag=false
if [[ "$EVENT" == push ]]; then
  [[ "$EVENT_REF" == "refs/tags/$RELEASE_TAG" ]]
  [[ "$(git rev-parse "refs/tags/$RELEASE_TAG^{commit}")" == "$(git rev-parse HEAD)" ]]
else
  [[ "$EVENT_REF" == "refs/heads/$DEFAULT_BRANCH" ]]
  [[ "$EVENT" == schedule || "$EVENT" == workflow_dispatch ]]
  if git show-ref --verify --quiet "refs/tags/$RELEASE_TAG"; then
    if [[ "$(git rev-parse "refs/tags/$RELEASE_TAG^{commit}")" != "$(git rev-parse "$EVENT_SHA^{commit}")" ]]; then
      echo "::error::Existing release tag does not point to the immutable event commit" >&2
      exit 1
    fi
  else
    [[ "$RELEASE_TAG" == "$(bash scripts/next-version.sh)" ]]
    create_tag=true
  fi
fi
echo "create_tag=$create_tag" >> "$GITHUB_OUTPUT"
