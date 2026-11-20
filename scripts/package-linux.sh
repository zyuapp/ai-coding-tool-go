#!/usr/bin/env bash
# Usage: scripts/package-linux.sh [out-dir]
# Builds AICodingTool-<version>-<arch>.tar.gz and .AppImage for this machine's architecture into
# out-dir (default build/package). Runs on Linux with GTK 3 and WebKitGTK 4.1 development files; the
# AppImage needs appimagetool on PATH. Set SKIP_FRONTEND=1 to reuse frontend/dist.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-$root/build/package}
app_name="AI Coding Tool"
app_id=com.zyuapp.aicodingtool
url_scheme=aicodingtool
executable=aicodingtool
[[ $(uname -s) == Linux ]] || { echo "package-linux.sh builds on Linux only." >&2; exit 1; }
case "$(uname -m)" in
  x86_64 | amd64) arch=x86_64 artifact_arch=x86_64 ;;
  aarch64 | arm64) arch=aarch64 artifact_arch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
version=$(sed -n 's/^ *"productVersion": "\(.*\)",*$/\1/p' "$root/wails.json" | head -1)
[[ -n $version ]] || { echo "Could not read the version from wails.json." >&2; exit 1; }
name=AICodingTool-$version-$artifact_arch

if [[ ${SKIP_FRONTEND:-} != 1 ]]; then
  echo "Building the renderer…"
  npm --prefix "$root/frontend" run build
fi

echo "Building the native helpers…"
(cd "$root" && go run ./native/build)
bash "$root/scripts/prepare-cua-driver.sh"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "Building the app binary $version…"
(cd "$root" && CGO_ENABLED=1 go build -trimpath -tags desktop,production,webkit2_41 \
  -ldflags "-w -s -X github.com/zyuapp/ai-coding-tool-go/internal/platform.packaged=true" \
  -o "$work/$executable" .)

# The binary finds its resources in ../Resources and its helpers in ./native.
appdir=$work/$name
mkdir -p "$appdir/usr/bin/native" "$appdir/usr/Resources" "$appdir/usr/share/applications" "$appdir/usr/share/icons/hicolor/512x512/apps"
install -m 755 "$work/$executable" "$appdir/usr/bin/$executable"
install -m 755 "$root/native/bin/aic-dictation-linux" "$appdir/usr/bin/native/aic-dictation-linux"
install -m 644 "$root/native/bin/libonnxruntime.so" "$appdir/usr/bin/native/libonnxruntime.so"
install -m 755 "$root/build/cua-driver/cua-driver" "$appdir/usr/Resources/cua-driver"
cp -R "$root/assets/app-plugin" "$appdir/usr/Resources/app-plugin"
cp -R "$root/assets/legal" "$appdir/usr/Resources/legal"
cp "$root/frontend/dist/legal/RENDERER-THIRD-PARTY-LICENSES.md" "$appdir/usr/Resources/legal/"
cp "$root/assets/icon.png" "$appdir/usr/share/icons/hicolor/512x512/apps/$app_id.png"
cat > "$appdir/usr/share/applications/$app_id.desktop" <<DESKTOP
[Desktop Entry]
Name=$app_name
Comment=Work with coding agents side by side.
Exec=$executable %U
Terminal=false
Type=Application
Icon=$app_id
Categories=Development;
StartupWMClass=$executable
MimeType=x-scheme-handler/$url_scheme;
DESKTOP

mkdir -p "$out"
echo "Writing $out/$name.tar.gz…"
tar -C "$appdir/usr" -czf "$out/$name.tar.gz" --transform "s,^\.,$name," .

if command -v appimagetool >/dev/null 2>&1; then
  ln -s "usr/bin/$executable" "$appdir/AppRun"
  cp "$appdir/usr/share/applications/$app_id.desktop" "$appdir/"
  cp "$root/assets/icon.png" "$appdir/$app_id.png"
  echo "Writing $out/$name.AppImage…"
  rm -f "$out/$name.AppImage"
  ARCH=$arch appimagetool --no-appstream "$appdir" "$out/$name.AppImage"
else
  echo "appimagetool is not on PATH; skipped the AppImage."
fi
echo "Built $out/$name.tar.gz"
