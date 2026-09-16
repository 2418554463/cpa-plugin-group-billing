#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repo_dir"
for tool in git jq zip tar; do
  command -v "$tool" >/dev/null || { echo "Missing tool: $tool" >&2; exit 1; }
done
if [[ -n "$(git status --porcelain)" ]]; then
  echo 'Commit all source changes before packaging; source and binary must match.' >&2
  exit 1
fi
version="$(awk -F'"' '/^[[:space:]]*Version[[:space:]]*=/ {print $2; exit}' internal/plugin/types.go)"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+-group\.[0-9]+$ ]] || { echo 'Invalid group version.' >&2; exit 1; }
source_commit="$(git rev-parse HEAD)"
[[ "$(git cat-file -t "refs/tags/v$version")" == tag ]] || { echo 'Create an annotated version tag before packaging.' >&2; exit 1; }
[[ "$(git rev-parse "v$version^{commit}")" == "$source_commit" ]] || { echo 'Version tag does not match HEAD.' >&2; exit 1; }
output_dir="$repo_dir/dist/releases/$version"
[[ ! -e "$output_dir" ]] || { echo "Refusing to overwrite $output_dir" >&2; exit 1; }

bash deploy/build-linux-amd64.sh
mkdir -p "$output_dir/bundle"
cp dist/linux-amd64/cpa-key-billing.so "$output_dir/bundle/"
cp deploy/README.md deploy/API.md deploy/VERIFICATION.md deploy/RELEASE_NOTES.md \
  deploy/plugins.example.yaml deploy/compose.override.example.yaml LICENSE "$output_dir/bundle/"
jq --arg commit "$source_commit" --arg tag "v$version" \
  '. + {source_commit: $commit, source_tag: $tag}' deploy/BUILDINFO.json > "$output_dir/bundle/BUILDINFO.json"

checksum() {
  if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}
(
  cd "$output_dir/bundle"
  checksum cpa-key-billing.so README.md API.md VERIFICATION.md RELEASE_NOTES.md \
    plugins.example.yaml compose.override.example.yaml LICENSE BUILDINFO.json > SHA256SUMS
  COPYFILE_DISABLE=1 tar -czf "$output_dir/cpa-key-billing_${version}_linux_amd64.tar.gz" \
    cpa-key-billing.so README.md API.md VERIFICATION.md RELEASE_NOTES.md \
    plugins.example.yaml compose.override.example.yaml LICENSE BUILDINFO.json SHA256SUMS
  zip -q -X -j "$output_dir/cpa-key-billing_${version}_linux_amd64.zip" cpa-key-billing.so
)
git archive --format=tar.gz --prefix="cpa-plugin-group-billing-$version/" \
  -o "$output_dir/cpa-plugin-group-billing_${version}_source.tar.gz" "$source_commit"
(
  cd "$output_dir"
  checksum "cpa-key-billing_${version}_linux_amd64.tar.gz" \
    "cpa-key-billing_${version}_linux_amd64.zip" \
    "cpa-plugin-group-billing_${version}_source.tar.gz" > checksums.txt
)
echo "Release assets: $output_dir"
