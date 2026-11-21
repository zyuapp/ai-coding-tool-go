#!/bin/zsh
# Usage: scripts/package-mac.sh [out-dir]
# Builds the installed app, "AI Coding Tool.app" (Apple silicon), into out-dir (default build/package):
# the production binary, its icon, the native helpers, the Cua Driver, the app plugin and the legal
# notices. It takes the Electron app's bundle id, URL scheme and profile, so it replaces that app.
# Signs with $MAC_SIGN_IDENTITY (a Developer ID Application identity, with a secure timestamp), else
# ad hoc. Set SKIP_FRONTEND=1 to reuse frontend/dist.
set -euo pipefail
root=${0:a:h:h}
out=${1:-$root/build/package}
app_name="AI Coding Tool"
bundle_id=com.zyuapp.aicodingtool
url_scheme=aicodingtool
executable=aicodingtool
minimum_macos=13.0
app="$out/$app_name.app"
sign_identity=${MAC_SIGN_IDENTITY:--}
sign_flags=(--force --sign $sign_identity --options runtime)
[[ $sign_identity == - ]] || sign_flags+=(--timestamp)

[[ $(uname -s) == Darwin ]] || { echo "package-mac.sh builds on macOS only." >&2; exit 1 }
version=$(sed -n 's/^ *"productVersion": "\(.*\)",*$/\1/p' $root/wails.json | head -1)
[[ -n $version ]] || { echo "Could not read the version from wails.json." >&2; exit 1 }

work=$(mktemp -d)
background=()
trap 'kill $background 2>/dev/null; rm -rf $work' EXIT

# The helpers and the icon do not depend on the renderer or the binary, so they build alongside them.
echo "Building the native helpers…"
(cd $root && go run ./native/build && zsh $root/scripts/prepare-cua-driver.sh) &
helpers=$!
background+=($helpers)

# The icon: Icon Composer's file when Xcode can compile it, else the flat PNG.
if xcrun -f actool >/dev/null 2>&1; then
  compiled_icon=1
  cp -R $root/assets/icon.icon $work/AppIcon.icon
  mkdir -p $work/icon
  xcrun actool $work/AppIcon.icon --compile $work/icon --app-icon AppIcon --enable-on-demand-resources NO \
    --development-region en --target-device mac --platform macosx --minimum-deployment-target $minimum_macos \
    --output-partial-info-plist $work/partial.plist >/dev/null &
else
  compiled_icon=0
  iconset=$work/AppIcon.iconset
  mkdir -p $iconset $work/icon
  (for size in 16 32 64 128 256 512; do
    sips -z $size $size $root/assets/icon.png --out $iconset/icon_${size}x${size}.png >/dev/null
    sips -z $((size * 2)) $((size * 2)) $root/assets/icon.png --out $iconset/icon_${size}x${size}@2x.png >/dev/null
  done
  iconutil -c icns $iconset -o $work/icon/AppIcon.icns) &
fi
icon=$!
background+=($icon)

if [[ ${SKIP_FRONTEND:-} != 1 ]]; then
  echo "Building the renderer…"
  npm --prefix $root/frontend run build
fi

echo "Building the app binary $version…"
(cd $root && CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
  CGO_CFLAGS="-mmacosx-version-min=$minimum_macos" \
  CGO_LDFLAGS="-framework UniformTypeIdentifiers -mmacosx-version-min=$minimum_macos" \
  go build -trimpath -tags desktop,production \
    -ldflags "-w -s -X github.com/zyuapp/ai-coding-tool-go/internal/platform.packaged=true" \
    -o $work/$executable .)

wait $helpers || { echo "The native helpers did not build." >&2; exit 1 }
wait $icon || { echo "The icon did not build." >&2; exit 1 }
background=()

echo "Assembling $app…"
rm -rf $app
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources/native"
cp $work/$executable "$app/Contents/MacOS/$executable"
cp $root/build/cua-driver/cua-driver "$app/Contents/Resources/cua-driver"
cp -R $root/assets/app-plugin "$app/Contents/Resources/app-plugin"
cp -R $root/assets/legal "$app/Contents/Resources/legal"
cp $root/frontend/dist/legal/RENDERER-THIRD-PARTY-LICENSES.md "$app/Contents/Resources/legal/"
for helper in aic-dictation aic-simulator; do
  [[ -f $root/native/bin/$helper ]] && cp $root/native/bin/$helper "$app/Contents/Resources/native/"
done

icon_plist=""
cp $work/icon/AppIcon.icns "$app/Contents/Resources/"
if [[ $compiled_icon == 1 ]]; then
  cp $work/icon/Assets.car "$app/Contents/Resources/"
  icon_plist="	<key>CFBundleIconName</key>
	<string>AppIcon</string>"
fi

cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleDisplayName</key>
	<string>$app_name</string>
	<key>CFBundleExecutable</key>
	<string>$executable</string>
	<key>CFBundleIconFile</key>
	<string>AppIcon</string>
$icon_plist
	<key>CFBundleIdentifier</key>
	<string>$bundle_id</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>$app_name</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>$version</string>
	<key>CFBundleVersion</key>
	<string>$version</string>
	<key>CFBundleURLTypes</key>
	<array>
		<dict>
			<key>CFBundleURLName</key>
			<string>$app_name project</string>
			<key>CFBundleURLSchemes</key>
			<array>
				<string>$url_scheme</string>
			</array>
		</dict>
	</array>
	<key>LSApplicationCategoryType</key>
	<string>public.app-category.developer-tools</string>
	<key>LSMinimumSystemVersion</key>
	<string>$minimum_macos</string>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSPrincipalClass</key>
	<string>NSApplication</string>
	<key>NSSupportsAutomaticGraphicsSwitching</key>
	<true/>
	<key>NSHumanReadableCopyright</key>
	<string>Copyright © Zhuocheng Yu</string>
	<key>NSMicrophoneUsageDescription</key>
	<string>$app_name listens while you dictate, and writes down what you say on this Mac.</string>
	<key>NSAppleEventsUsageDescription</key>
	<string>$app_name uses Automation only when a requested computer-use action needs to communicate with another app.</string>
	<key>NSScreenCaptureUsageDescription</key>
	<string>$app_name captures app windows so Claude can see and interact with interfaces you ask it to control.</string>
</dict>
</plist>
PLIST
printf 'APPL????' > "$app/Contents/PkgInfo"

echo "Signing with ${sign_identity/#-/an ad hoc signature}…"
entitlements=$work/entitlements.plist
cat > $entitlements <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>com.apple.security.cs.disable-library-validation</key>
	<true/>
	<key>com.apple.security.device.audio-input</key>
	<true/>
</dict>
</plist>
PLIST
for helper in "$app"/Contents/Resources/native/*(N); do
  codesign $sign_flags --entitlements $entitlements $helper
done
# The Cua Driver keeps its own signature, which its permissions are tied to.
codesign $sign_flags --entitlements $entitlements "$app"
codesign --verify --strict "$app"
echo "Built $app"
