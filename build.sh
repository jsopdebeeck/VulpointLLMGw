#!/usr/bin/env bash
# LLMGW by Vulpoint.com : construit les livrables dans dist/.
#
#   ./build.sh [--linux-only | --mac-only] [--skip-tests]
#
#   --linux-only   seulement les archives Linux (amd64 et arm64)
#   --mac-only     seulement LLMGW.app et le DMG (macapp/build.sh, sur un Mac avec swiftc)
#   --skip-tests   ne lance pas go vet et go test avant de compiler
#
# Linux : dist/llmgw-<version>-linux-<arch>.tar.gz, un dossier llmgw-<version>/ avec le binaire
# statique (CGO_ENABLED=0, SQLite en Go pur), config.example.toml, setup.sh, llmgw.service,
# Caddyfile et LISEZMOI.txt. Installation : voir LISEZMOI.txt, ou deploy/deploy.sh.
# Mac : dist/LLMGW.app et dist/LLMGW-<version>.dmg. Empreintes de la version : dist/SHA256SUMS.
set -euo pipefail
cd "$(dirname "$0")"

mac=1 linux=1 tests=1
while [ $# -gt 0 ]; do
	case "$1" in
	--linux-only) mac=0; shift ;;
	--mac-only) linux=0; shift ;;
	--skip-tests) tests=0; shift ;;
	-h | --help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
	*) echo "option inconnue : $1 (--help)" >&2; exit 2 ;;
	esac
done
[ "$mac$linux" != 00 ] || { echo "--linux-only et --mac-only sont incompatibles" >&2; exit 2; }

version=$(sed -n 's/^const Version = "\(.*\)"/\1/p' main.go)
[ -n "$version" ] || { echo "const Version introuvable dans main.go" >&2; exit 1; }
echo "LLMGW $version"

if [ $mac = 1 ] && [ "$(uname -s)" != Darwin ]; then
	[ $linux = 1 ] || { echo "LLMGW.app ne se construit que sur un Mac" >&2; exit 1; }
	echo "! pas sur un Mac : LLMGW.app et DMG non construits"
	mac=0
fi

if [ $tests = 1 ]; then
	echo "→ go vet et go test"
	go vet ./...
	go test ./...
fi

mkdir -p dist

if [ $mac = 1 ]; then
	echo "→ LLMGW.app et DMG (macapp/build.sh)"
	macapp/build.sh
fi

if [ $linux = 1 ]; then
	bash -n deploy/setup.sh
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	# archive propre pour Linux : ni attributs étendus ni fichiers ._ du Mac, propriétaire root
	export COPYFILE_DISABLE=1
	if tar --version 2>/dev/null | grep -q GNU; then
		tar_opts=(--format=ustar --owner=0 --group=0 --numeric-owner)
	else
		tar_opts=(--format ustar --uid 0 --gid 0 --uname root --gname root --no-xattrs --no-acls --no-fflags)
		[ "$(uname -s)" != Darwin ] || tar_opts+=(--no-mac-metadata)
	fi
	name=llmgw-$version
	for arch in amd64 arm64; do
		echo "→ linux/$arch"
		pkg=$tmp/$arch/$name
		mkdir -p "$pkg"
		CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$pkg/llmgw" .
		cp config.example.toml deploy/setup.sh deploy/llmgw.service deploy/Caddyfile "$pkg/"
		sed -e "s/LLMGW_VERSION/$version/g" -e "s/LLMGW_ARCH/$arch/g" deploy/LISEZMOI.txt >"$pkg/LISEZMOI.txt"
		chmod 644 "$pkg"/*
		chmod 755 "$pkg" "$pkg/llmgw" "$pkg/setup.sh"
		tar "${tar_opts[@]}" -czf "dist/$name-linux-$arch.tar.gz" -C "$tmp/$arch" "$name"
	done
fi

# empreintes de tous les livrables présents pour cette version
cd dist
files=()
for f in "llmgw-$version-linux-amd64.tar.gz" "llmgw-$version-linux-arm64.tar.gz" "LLMGW-$version.dmg"; do
	[ ! -f "$f" ] || files+=("$f")
done
if [ ${#files[@]} -gt 0 ]; then
	if command -v sha256sum >/dev/null; then sha256sum "${files[@]}" >SHA256SUMS; else shasum -a 256 "${files[@]}" >SHA256SUMS; fi
fi
echo "OK : $PWD"
ls -lh ${files[@]+"${files[@]}"} SHA256SUMS 2>/dev/null | awk '{ printf "     %-34s %s\n", $NF, $5 }'
