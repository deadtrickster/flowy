#!/usr/bin/env bash
#
# Vendor the draw.io editor into web/public/drawio/.
#
# The console must build with no network, so the editor is committed rather
# than fetched at build time. This script is how that tree was produced and how
# it gets re-produced on a version bump - it is not part of the build.
#
# What it takes is not "the release". The release unpacks to 152 MB; the embed
# editor only ever asks for the 23 paths in drawio.manifest, which is 24.7 MB.
# That list was measured, not guessed: web/scripts/drawio-probe.mjs serves the
# unpacked release, drives the editor in a real browser with the shape palettes
# open, and records every request. Re-run it after a bump - if drawio starts
# asking for a file that is not vendored the editor 404s at runtime, and the
# probe is what catches that before a user does.
#
# What is deliberately NOT vendored: stencils/ (42 MB) and img/ (12 MB), the
# shape XML and clipart behind "More Shapes". The default palettes draw
# themselves from JS and need neither. Opening More Shapes and picking one of
# the extra libraries (AWS, Azure, network...) will 404 - that is the price of
# not putting 54 MB of clipart in the binary.
#
# Usage: web/scripts/vendor-drawio.sh [version]

set -euo pipefail

VERSION="${1:-31.1.8}"
WAR_SHA256_31_1_8="46389bd60810f9775cd463c9eff4f4f8335f10926d613b0606ad4e978f46d49b"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
web="$(dirname "$here")"
dest="$web/public/drawio"
manifest="$here/drawio.manifest"

if [ ! -f "$manifest" ]; then
	echo "no manifest at $manifest" >&2
	exit 1
fi

for tool in curl unzip sha256sum; do
	command -v "$tool" >/dev/null || {
		echo "need $tool" >&2
		exit 1
	}
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

url="https://github.com/jgraph/drawio/releases/download/v${VERSION}/draw.war"
echo "fetching $url"
curl -fsSL -o "$work/draw.war" "$url"

# Pin the bytes for the version this tree was built from. A different version
# is allowed - it just cannot claim to be this one.
if [ "$VERSION" = "31.1.8" ]; then
	got="$(sha256sum "$work/draw.war" | cut -d' ' -f1)"
	if [ "$got" != "$WAR_SHA256_31_1_8" ]; then
		echo "draw.war sha256 mismatch for v${VERSION}" >&2
		echo "  want $WAR_SHA256_31_1_8" >&2
		echo "  got  $got" >&2
		exit 1
	fi
	echo "sha256 ok"
fi

unzip -q "$work/draw.war" -d "$work/war"

rm -rf "$dest"
mkdir -p "$dest"

count=0
while read -r path; do
	# Skip blanks and comments so the manifest can explain itself.
	case "$path" in
	'' | '#'*) continue ;;
	esac
	src="$work/war/${path#/}"
	if [ ! -f "$src" ]; then
		echo "manifest lists $path but v${VERSION} does not have it" >&2
		exit 1
	fi
	out="$dest/${path#/}"
	mkdir -p "$(dirname "$out")"
	cp "$src" "$out"
	count=$((count + 1))
done <"$manifest"

# index.html ships as drawio wrote it apart from two edits, both of which are
# about this being an editor inside another application rather than a site.
#
#   1. the canonical link points at app.diagrams.net, and a canonical pointing
#      off-node is a link this console should not be making.
#   2. drawio registers a service worker so it can install itself for offline
#      use. It is already offline here, the file is not vendored, and the
#      registration only ever produced a SecurityError in the console - so it
#      is turned off at the source rather than papered over by shipping a dead
#      worker. The stub never settles rather than rejecting, because a
#      rejection drawio does not catch is just a different console error.
#
# Everything else is left alone so a version bump is a re-run, not a re-patch.
if [ -f "$dest/index.html" ]; then
	sed -i '/rel="canonical"/d' "$dest/index.html"
	sed -i 's#<script src="js/bootstrap.js"></script>#<script>if(navigator.serviceWorker\&\&navigator.serviceWorker.register){navigator.serviceWorker.register=function(){return new Promise(function(){})}}</script>\n\t<script src="js/bootstrap.js"></script>#' "$dest/index.html"
	grep -q "navigator.serviceWorker.register=function" "$dest/index.html" || {
		echo "index.html: could not disable the service worker registration" >&2
		exit 1
	}
fi

curl -fsSL -o "$dest/LICENSE" \
	"https://raw.githubusercontent.com/jgraph/drawio/v${VERSION}/LICENSE"

cat >"$dest/VENDORED.md" <<EOF
draw.io ${VERSION}, vendored from jgraph/drawio, Apache License 2.0 (see LICENSE).

Produced by web/scripts/vendor-drawio.sh from the v${VERSION} draw.war release
asset, taking only the paths in web/scripts/drawio.manifest. Do not edit these
files by hand - re-run the script.

Not vendored: stencils/ and img/, the shape XML and clipart behind "More
Shapes". The default palettes need neither.
EOF

echo "vendored $count files into $dest"
du -sh "$dest"
