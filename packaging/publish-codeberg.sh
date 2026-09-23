#!/bin/sh
# Publishes dist/ (from packaging/release.sh) as the Codeberg release for a tag:
#
#   CODEBERG_TOKEN=… packaging/publish-codeberg.sh v0.2.0
#
# The release is created as a draft, the files are uploaded (replacing any with the same name,
# so a re-run is safe), and only then is it published: nobody sees a half-uploaded release.
# The notes list the commits since the previous tag.
set -eu
tag="$1"
repo="${CODEBERG_REPO:-b-wisman/clickup-tui}"
api="${CODEBERG_API:-https://codeberg.org/api/v1}/repos/$repo"
: "${CODEBERG_TOKEN:?set CODEBERG_TOKEN}"
cd "$(dirname "$0")/.."

call() { # method path [curl args…]
  method="$1" path="$2"
  shift 2
  curl -fsS -X "$method" -H "Authorization: token $CODEBERG_TOKEN" "$@" "$api$path"
}

previous="$(git describe --tags --abbrev=0 "$tag^" 2>/dev/null || true)"
range="${previous:+$previous..}$tag"
notes="$(git log --no-merges --pretty='- %s' "$range")

**Install:**
- macOS, Linux: \`curl -fsSL https://codeberg.org/$repo/raw/branch/main/install.sh | sh\`
- Windows: \`irm https://codeberg.org/$repo/raw/branch/main/install.ps1 | iex\`

Check downloads against \`checksums.txt\`."
prerelease=false
case "$tag" in *-*) prerelease=true ;; esac

if release="$(call GET "/releases/tags/$tag" 2>/dev/null)"; then
  echo "Updating the release for $tag"
else
  body="$(jq -n --arg tag "$tag" --arg notes "$notes" --argjson pre "$prerelease" \
    '{tag_name: $tag, name: $tag, body: $notes, draft: true, prerelease: $pre}')"
  release="$(call POST /releases -H 'Content-Type: application/json' -d "$body")"
  echo "Created a draft release for $tag"
fi
id="$(printf '%s' "$release" | jq -r .id)"

for file in dist/*; do
  name="$(basename "$file")"
  old="$(printf '%s' "$release" | jq -r --arg n "$name" '.assets[]? | select(.name == $n) | .id')"
  if [ -n "$old" ]; then
    call DELETE "/releases/$id/assets/$old" >/dev/null
  fi
  call POST "/releases/$id/assets?name=$name" -F "attachment=@$file" >/dev/null
  echo "  uploaded $name"
done

call PATCH "/releases/$id" -H 'Content-Type: application/json' -d '{"draft": false}' >/dev/null
echo "Published https://codeberg.org/$repo/releases/tag/$tag"
