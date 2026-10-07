#!/bin/zsh
# Builds LLMGW.app (menu bar app) next to config.toml, and the llmgw binary it starts.
# Needs Go and the Xcode Command Line Tools (xcode-select --install).
set -euo pipefail
cd "${0:A:h}/.."

version=$(sed -n 's/^const Version = "\(.*\)"/\1/p' main.go)
echo "LLMGW $version"

go build -o llmgw .

app=LLMGW.app
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"

swiftc -O -swift-version 5 -parse-as-library -target "$(uname -m)-apple-macos13" \
	-o "$app/Contents/MacOS/LLMGW" macapp/LLMGW.swift

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

codesign --force --sign - "$app" # local ad-hoc signature
echo "OK : $PWD/$app (double-clic pour lancer, icône dans la barre des menus)"
