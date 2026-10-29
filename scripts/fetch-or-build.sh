#!/bin/sh
# Install the herdr-park binary into bin/.
#
# Downloads the release matching this checkout's manifest version and checks
# it against the release's SHA256SUMS. If that is not possible (unsupported
# platform, no release yet, network or checksum failure), builds from source
# with Go instead.
set -u

repo="andymartinezot/herdr-park"
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
out="$root/bin/herdr-park"
base_url="https://github.com/$repo/releases/download"

cleanup() { [ -n "${tmpdir:-}" ] && rm -rf "$tmpdir"; }
trap cleanup EXIT

build_from_source() {
  cleanup
  if ! command -v go >/dev/null 2>&1; then
    echo "herdr-park: no matching prebuilt binary and Go is not installed (https://go.dev/dl)." >&2
    exit 1
  fi
  mkdir -p "$root/bin" || exit 1
  cd "$root" || exit 1
  exec go build -trimpath -ldflags "-s -w -X main.version=${version:-dev}" -o "$out" ./cmd/herdr-park
}

fallback() {
  echo "herdr-park: $1; building from source instead." >&2
  build_from_source
}

download() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$base_url/v$version/$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$base_url/v$version/$1"
  else
    return 127
  fi
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    return 127
  fi
}

version=$(sed -n 's/^version *= *"\([^"]*\)".*/\1/p' "$root/herdr-plugin.toml" | head -n 1)
[ -n "$version" ] || fallback "could not read the version from herdr-plugin.toml"

case "$(uname -s)/$(uname -m)" in
  Darwin/arm64 | Darwin/aarch64) target="darwin_arm64" ;;
  Darwin/x86_64 | Darwin/amd64) target="darwin_amd64" ;;
  Linux/aarch64 | Linux/arm64) target="linux_arm64" ;;
  Linux/x86_64 | Linux/amd64) target="linux_amd64" ;;
  *) fallback "no prebuilt binary for $(uname -s)/$(uname -m)" ;;
esac

asset="herdr-park_${target}"
tmpdir=$(mktemp -d 2>/dev/null) || fallback "could not create a temporary directory"
download SHA256SUMS "$tmpdir/SHA256SUMS" || fallback "no release v$version"
download "$asset" "$tmpdir/$asset" || fallback "no $asset in release v$version"

expected=$(awk -v a="$asset" '$2 == a || $2 == "*" a {print tolower($1); exit}' "$tmpdir/SHA256SUMS")
[ -n "$expected" ] || fallback "release v$version lists no checksum for $asset"
actual=$(sha256_of "$tmpdir/$asset") || fallback "no SHA-256 tool available"
[ "$actual" = "$expected" ] || fallback "checksum mismatch for $asset"

mkdir -p "$root/bin" && chmod +x "$tmpdir/$asset" && mv -f "$tmpdir/$asset" "$out" ||
  fallback "could not install the downloaded binary"
echo "herdr-park: installed verified v$version ($target)."
