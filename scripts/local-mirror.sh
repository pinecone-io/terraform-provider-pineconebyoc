#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/local-mirror.sh [--rebuild] [--version X.Y.Z] [--dir DIR]

Builds the provider for this machine into a filesystem mirror, and writes a CLI configuration that
installs pinecone-io/pineconebyoc from that mirror while every other provider still comes from its
registry. Terraform and OpenTofu both read it. Roots keep their normal provider source and version,
so they switch to a published release by dropping TF_CLI_CONFIG_FILE.

  --version  the version to build as (default 0.1.0)
  --dir      the mirror directory (default .local-mirror in this repo)
  --rebuild  build even when the source has not changed

An existing build is reused until the source changes. A root initialised against an older build of
the same version then needs its .terraform.lock.hcl removed before init, because the lock file pins
that build's checksum.
USAGE
}

root="$(cd "$(dirname "$0")/.." && pwd)"
version=0.1.0
dir="$root/.local-mirror"
rebuild=

while [ $# -gt 0 ]; do
  case "$1" in
    -h | --help) usage; exit 0 ;;
    --rebuild) rebuild=1; shift ;;
    --version) version="${2:?--version needs a value}"; shift 2 ;;
    --dir) dir="${2:?--dir needs a value}"; shift 2 ;;
    *) usage >&2; exit 2 ;;
  esac
done

if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "version $version is not semver, which Terraform requires of a mirrored provider" >&2
  exit 2
fi

mkdir -p "$dir"
dir="$(cd "$dir" && pwd)"
platform="$(go env GOOS)_$(go env GOARCH)"
hosts=(registry.terraform.io registry.opentofu.org)
name="terraform-provider-pineconebyoc_v$version"
binary="$dir/${hosts[0]}/pinecone-io/pineconebyoc/$version/$platform/$name"

changed() {
  find "$root" \( -path "$root/.git" -o -path "$dir" -o -path "$root/dist" \) -prune -o \
    \( -name '*.go' -o -name go.mod -o -name go.sum \) -newer "$binary" -print -quit
}

if [ -f "$binary" ] && [ -z "$rebuild" ] && [ -z "$(changed)" ]; then
  echo "reusing the existing build at $binary; pass --rebuild to replace it"
else
  mkdir -p "$(dirname "$binary")"
  (cd "$root" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$binary" .)
  echo "built pineconebyoc $version for $platform into $binary"
  echo "a root initialised against an older build of $version needs rm .terraform.lock.hcl before init"
fi

for host in "${hosts[@]:1}"; do
  target="$dir/$host/pinecone-io/pineconebyoc/$version/$platform"
  mkdir -p "$target"
  cp "$binary" "$target/$name"
  echo "served the same build for OpenTofu from $target"
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

echo "To use the local provider in this shell:"
echo "  export TF_CLI_CONFIG_FILE=$dir/terraformrc"
