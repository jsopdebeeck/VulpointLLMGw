#!/bin/zsh
# Builds the self-contained LLMGW.app (menu bar app + embedded gateway, universal arm64 / x86_64)
# in dist/, and a DMG to install it. Needs Go and the Xcode Command Line Tools (xcode-select --install).
# The app depends on nothing else: the gateway is a static Go binary (pure-Go SQLite).
set -euo pipefail
cd "${0:A:h}/.."

version=$(sed -n 's/^const Version = "\(.*\)"/\1/p' main.go)
echo "LLMGW $version"

mkdir -p dist
app=dist/LLMGW.app
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"

# gateway: one binary per architecture, merged into a universal binary
for arch in arm64 amd64; do
	CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$tmp/llmgw-$arch" .
done
lipo -create -output "$app/Contents/Resources/llmgw" "$tmp/llmgw-arm64" "$tmp/llmgw-amd64"
cp config.example.toml "$app/Contents/Resources/config.example.toml"

# menu bar app, same for both architectures
# (x86_64: the Command Line Tools ship libswiftCompatibilityPacks for arm64 only; the Intel build
# does not need it, so the linker warning about it is silenced)
swiftc -O -swift-version 5 -parse-as-library -target arm64-apple-macos13 -o "$tmp/LLMGW-arm64" macapp/LLMGW.swift
swiftc -O -swift-version 5 -parse-as-library -target x86_64-apple-macos13 -Xlinker -w -o "$tmp/LLMGW-x86_64" macapp/LLMGW.swift
lipo -create -output "$app/Contents/MacOS/LLMGW" "$tmp/LLMGW-arm64" "$tmp/LLMGW-x86_64"

# icon: the Vulpoint logo in every size macOS asks for
set=$tmp/LLMGW.iconset
mkdir -p "$set"
for s in 16 32 128 256 512; do
	sips -z $s $s docs/img/vulpoint-logo.jpg -s format png --out "$set/icon_${s}x${s}.png" >/dev/null
	d=$((s * 2))
	sips -z $d $d docs/img/vulpoint-logo.jpg -s format png --out "$set/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$set" -o "$app/Contents/Resources/LLMGW.icns"

cat > "$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
	<key>CFBundleName</key><string>LLMGW</string>
	<key>CFBundleDisplayName</key><string>LLMGW by Vulpoint.com</string>
	<key>CFBundleIdentifier</key><string>com.vulpoint.llmgw</string>
	<key>CFBundleExecutable</key><string>LLMGW</string>
	<key>CFBundleIconFile</key><string>LLMGW</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$version</string>
	<key>CFBundleVersion</key><string>$version</string>
	<key>LSMinimumSystemVersion</key><string>13.0</string>
	<key>LSUIElement</key><true/>
	<key>NSHumanReadableCopyright</key><string>LLMGW by Vulpoint.com</string>
</dict></plist>
EOF

# local ad-hoc signature: the embedded binary first, then the bundle
codesign --force --sign - "$app/Contents/Resources/llmgw"
codesign --force --sign - "$app"

# DMG: the app and a shortcut to Applications, to install by drag and drop
dmg=dist/LLMGW-$version.dmg
mkdir -p "$tmp/dmg"
cp -R "$app" "$tmp/dmg/"
ln -s /Applications "$tmp/dmg/Applications"
rm -f "$dmg"
hdiutil create -quiet -volname "LLMGW $version" -srcfolder "$tmp/dmg" -ov -format UDZO "$dmg"

echo "OK : $PWD/$app"
echo "     $PWD/$dmg"
