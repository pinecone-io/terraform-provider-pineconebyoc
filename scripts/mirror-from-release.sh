#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/mirror-from-release.sh --version X.Y.Z [--dir DIR] [--repo OWNER/REPO] [--from-dist DIR]

Downloads a release's zips with gh release download into a provider mirror, checks each against the
release's SHA256SUMS, and writes a CLI configuration that installs pinecone-io/pineconebyoc from that
mirror while every other provider still comes from its registry. Terraform and OpenTofu both read it.

The mirror is in the packed layout, with the network mirror protocol's index.json and <version>.json
next to the zips. The same directory works as a filesystem mirror, or served over HTTPS as a network
mirror. Run it again with another version to add that version.

  --version    the release to mirror, with or without the leading v (required)
  --dir        the mirror directory (default ~/.terraform.d/pineconebyoc-mirror)
  --repo       the repository that publishes the release (default pinecone-io/terraform-provider-pineconebyoc)
  --from-dist  read the zips from a goreleaser output directory such as dist/, not from a release
USAGE
}

version=
dir="$HOME/.terraform.d/pineconebyoc-mirror"
repo=pinecone-io/terraform-provider-pineconebyoc
from=

while [ $# -gt 0 ]; do
  case "$1" in
    -h | --help) usage; exit 0 ;;
    --version) version="${2:?--version needs a value}"; shift 2 ;;
    --dir) dir="${2:?--dir needs a value}"; shift 2 ;;
    --repo) repo="${2:?--repo needs a value}"; shift 2 ;;
    --from-dist) from="${2:?--from-dist needs a value}"; shift 2 ;;
    *) usage >&2; exit 2 ;;
  esac
done

if [ -z "$version" ]; then
  usage >&2
  exit 2
fi
version="${version#v}"
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "version $version is not semver, which Terraform requires of a mirrored provider" >&2
  exit 2
fi

project=terraform-provider-pineconebyoc
sums="${project}_${version}_SHA256SUMS"

if [ -n "$from" ]; then
  src="$(cd "$from" && pwd)"
  echo "reading $version from $src, not from a release"
else
  src="$(mktemp -d)"
  trap 'rm -rf "$src"' EXIT
  gh release download "v$version" -R "$repo" -D "$src" -p "${project}_${version}_*.zip" -p "$sums"
  echo "downloaded release v$version from $repo"
fi

if [ ! -f "$src/$sums" ]; then
  echo "no $sums in $src" >&2
  exit 1
fi
shopt -s nullglob
zips=("$src/${project}_${version}_"*.zip)
if [ ${#zips[@]} -eq 0 ]; then
  echo "no $version zips in $src" >&2
  exit 1
fi

sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}

archives=
for zip in "${zips[@]}"; do
  name="$(basename "$zip")"
  want="$(awk -v f="$name" '$2 == f { print $1 }' "$src/$sums")"
  if [ -z "$want" ] || [ "$(sha256 "$zip")" != "$want" ]; then
    echo "$name does not match $sums, refusing to mirror it" >&2
    exit 1
  fi
  platform="${name#"${project}_${version}_"}"
  archives+="${archives:+$',\n'}    \"${platform%.zip}\": {\"url\": \"$name\", \"hashes\": [\"zh:$want\"]}"
done
echo "checked ${#zips[@]} zips against $sums"

mkdir -p "$dir"
dir="$(cd "$dir" && pwd)"
hosts=(registry.terraform.io registry.opentofu.org)
for host in "${hosts[@]}"; do
  target="$dir/$host/pinecone-io/pineconebyoc"
  mkdir -p "$target"
  cp "${zips[@]}" "$target/"
  printf '{\n  "archives": {\n%s\n  }\n}\n' "$archives" >"$target/$version.json"

  versions=
  for f in "$target"/*.json; do
    v="$(basename "$f" .json)"
    [ "$v" = index ] && continue
    versions+="${versions:+, }\"$v\": {}"
  done
  printf '{"versions": {%s}}\n' "$versions" >"$target/index.json"
  echo "mirrored $version into $target"
done

if [ -f "$dir/terraformrc" ]; then
  echo "keeping the existing $dir/terraformrc"
else
  cat >"$dir/terraformrc" <<RC
provider_installation {
  filesystem_mirror {
    path    = "$dir"
    include = ["registry.terraform.io/pinecone-io/pineconebyoc", "registry.opentofu.org/pinecone-io/pineconebyoc"]
  }
  direct {
    exclude = ["registry.terraform.io/pinecone-io/pineconebyoc", "registry.opentofu.org/pinecone-io/pineconebyoc"]
  }
}
RC
  echo "wrote $dir/terraformrc"
fi

echo "To install from the mirror in this shell:"
echo "  export TF_CLI_CONFIG_FILE=$dir/terraformrc"
