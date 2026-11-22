#!/usr/bin/env bash
# Packages the Wails desktop shell into a Sumi.app bundle for one architecture.
# Kept as a script so local builds and CI produce byte-identical bundles.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version=""
goarch="$(go env GOARCH)"
out="$root/bin/Sumi.app"
zip=""

usage() {
  echo "usage: $0 --version <semver> [--goarch arm64|amd64] [--out <path>/Sumi.app] [--zip <path>.zip]" >&2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) version="$2"; shift 2 ;;
    --goarch) goarch="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --zip) zip="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 2 ;;
  esac
done

if [ -z "$version" ]; then
  usage
  exit 2
fi

case "$goarch" in
  arm64) cc=""; arch_flags="" ;;
  # Go cross-compiles darwin/amd64 from an arm64 runner by targeting the host SDK with -arch.
  amd64) cc="clang -arch x86_64"; arch_flags=" -arch x86_64" ;;
  *) echo "unsupported goarch: $goarch" >&2; exit 2 ;;
esac

binary="$root/bin/sumi-desktop-$goarch"
ldflags="-X github.com/abcdlsj/sumi/internal/version.Version=$version"

echo "building desktop shell (goarch=$goarch version=$version)" >&2
(
  cd "$root/desktop"
  # shellcheck disable=SC2086
  env GOARCH="$goarch" CGO_ENABLED=1 ${cc:+CC="$cc"} CGO_CFLAGS="${arch_flags# }" \
    CGO_LDFLAGS="${arch_flags# } -framework UniformTypeIdentifiers" \
    go build -tags production -trimpath -ldflags "$ldflags" -o "$binary" .
)

staging="$out.staging"
rm -rf "$staging"
mkdir -p "$staging/Contents/MacOS" "$staging/Contents/Resources"
cp "$binary" "$staging/Contents/MacOS/Sumi"
cp "$root/desktop/Info.plist" "$staging/Contents/Info.plist"
plutil -replace CFBundleVersion -string "$version" "$staging/Contents/Info.plist"
plutil -replace CFBundleShortVersionString -string "$version" "$staging/Contents/Info.plist"
cp "$root/desktop/AppIcon.icns" "$staging/Contents/Resources/AppIcon.icns"
# Ad-hoc signing keeps the bundle launchable on the build machine; release signing
# would replace this with a Developer ID identity plus notarization.
codesign --force --deep --sign - "$staging"

rm -rf "$out"
mv "$staging" "$out"

if [ -n "$zip" ]; then
  mkdir -p "$(dirname "$zip")"
  rm -f "$zip"
  # ditto preserves resources and the signature; zip(1) would drop both.
  ditto -c -k --sequesterRsrc --keepParent "$out" "$zip"
  echo "packaged: $zip" >&2
fi

echo "desktop app: $out" >&2
