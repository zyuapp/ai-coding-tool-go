#!/usr/bin/env bash
# Usage: scripts/package-linux.sh [out-dir]
# Builds AICodingTool-<version>-<arch>.tar.gz, .deb and .AppImage for this machine's architecture into
# out-dir (default build/package). Runs on Linux with GTK 3 and WebKitGTK 4.1 development files; the
# .deb needs dpkg-deb and the AppImage appimagetool on PATH. Set SKIP_FRONTEND=1 to reuse frontend/dist.
# scripts/linux-docker.sh package runs it in a container with all of these.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-$root/build/package}
app_name="AI Coding Tool"
app_id=com.zyuapp.aicodingtool
url_scheme=aicodingtool
executable=aicodingtool
[[ $(uname -s) == Linux ]] || { echo "package-linux.sh builds on Linux only." >&2; exit 1; }
case "$(uname -m)" in
  x86_64 | amd64) arch=x86_64 artifact_arch=x86_64 deb_arch=amd64 ;;
  aarch64 | arm64) arch=aarch64 artifact_arch=arm64 deb_arch=arm64 ;;
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
Comment=A fast local desktop client for Claude and Codex coding tasks.
Exec=$executable %U
Terminal=false
Type=Application
Icon=$app_id
Categories=Development;
StartupWMClass=$app_id
MimeType=x-scheme-handler/$url_scheme;
DESKTOP

mkdir -p "$out"

write_tar() {
  echo "Writing $out/$name.tar.gz…"
  tar -C "$appdir/usr" -czf "$out/$name.tar.gz" --transform "s,^\.,$name," .
}

# The Electron app's package name and folder, so installing this one replaces it in place.
write_deb() {
  if ! command -v dpkg-deb >/dev/null 2>&1; then
    echo "dpkg-deb is not on PATH; skipped the .deb."
    return
  fi
  deb=$work/deb
  install_dir="/opt/$app_name"
  mkdir -p "$deb$install_dir" "$deb/usr/bin" "$deb/DEBIAN"
  cp -R "$appdir/usr/." "$deb$install_dir/"
  ln -s "$install_dir/bin/$executable" "$deb/usr/bin/$executable"
  mkdir -p "$deb/usr/share"
  cp -R "$appdir/usr/share/applications" "$appdir/usr/share/icons" "$deb/usr/share/"
  cat > "$deb/DEBIAN/control" <<CONTROL
Package: ai-coding-tool
Version: $version
Section: devel
Priority: optional
Architecture: $deb_arch
Maintainer: Zhuocheng Yu <zhuocheng.yu.d@gmail.com>
Installed-Size: $(du -sk "$deb" | cut -f1)
Depends: libgtk-3-0, libwebkit2gtk-4.1-0, ca-certificates, xdg-utils, x11-utils, libsecret-tools
Recommends: pulseaudio-utils | alsa-utils, xdotool
Homepage: https://github.com/zyuapp/ai-coding-tool-go
Description: $app_name
 A fast local desktop client for Claude and Codex coding tasks.
CONTROL
  # Replacing the Electron package runs its postrm, which removes /usr/bin/$executable through
  # update-alternatives after this package has unpacked it.
  cat > "$deb/DEBIAN/postinst" <<POSTINST
#!/bin/sh
set -e
ln -sf "$install_dir/bin/$executable" "/usr/bin/$executable"
POSTINST
  chmod 755 "$deb/DEBIAN/postinst"
  echo "Writing $out/$name.deb…"
  dpkg-deb --build --root-owner-group "$deb" "$out/$name.deb"
}

write_appimage() {
  if ! command -v appimagetool >/dev/null 2>&1; then
    echo "appimagetool is not on PATH; skipped the AppImage."
    return
  fi
  ln -s "usr/bin/$executable" "$appdir/AppRun"
  cp "$appdir/usr/share/applications/$app_id.desktop" "$appdir/"
  cp "$root/assets/icon.png" "$appdir/$app_id.png"
  echo "Writing $out/$name.AppImage…"
  rm -f "$out/$name.AppImage"
  ARCH=$arch appimagetool --no-appstream "$appdir" "$out/$name.AppImage"
}

# Each package only reads $appdir/usr, so they are written at once.
write_tar & tar_job=$!
write_deb & deb_job=$!
write_appimage & appimage_job=$!
wait $tar_job || { echo "The .tar.gz was not written." >&2; exit 1; }
wait $deb_job || { echo "The .deb was not written." >&2; exit 1; }
wait $appimage_job || { echo "The AppImage was not written." >&2; exit 1; }
echo "Built $name in $out"
