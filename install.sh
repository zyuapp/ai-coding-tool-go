#!/bin/sh
# Install the latest AI Coding Tool release.
#
#   curl -fsSL https://raw.githubusercontent.com/zyuapp/ai-coding-tool-go/main/install.sh | sh
#
# macOS gets the app in /Applications. Linux gets the AppImage in ~/Applications, which is the only
# Linux package that updates itself without asking for a password.
set -eu

REPO="zyuapp/ai-coding-tool-go"
DOWNLOAD="https://github.com/$REPO/releases/latest/download"
APP_NAME="AI Coding Tool"
BUNDLE_ID=com.zyuapp.aicodingtool
# The Electron app's .deb uses the same package name and folder.
DEB_PACKAGE=ai-coding-tool
DEB_DIR="/opt/AI Coding Tool"

fail() { printf '\nInstall failed: %s\n' "$1" >&2; exit 1; }
say() { printf '%s\n' "$1"; }

# Each platform's update feed (scripts/release-feed.sh) names its packages and their checksums.
feed_for() {
  case "$1" in
    Darwin)
      if [ "$2" = "x86_64" ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = "1" ]; then
        set -- "$1" arm64
      fi
      [ "$2" = "arm64" ] || fail "macOS builds are Apple silicon only, and this Mac is $2."
      echo latest-mac.yml
      ;;
    Linux)
      case "$2" in
        x86_64 | amd64) echo latest-linux.yml ;;
        aarch64 | arm64) echo latest-linux-arm64.yml ;;
        *) fail "Linux builds cover x86_64 and arm64, and this machine is $2." ;;
      esac
      ;;
    *) fail "$1 is not supported yet." ;;
  esac
}

# Prints "<file> <sha512>" for the feed's first file ending in $1.
pick() {
  printf '%s\n' "$feed" | awk -v suffix="$1" '
    $1 == "-" && $2 == "url:" { file = $3; next }
    $1 == "sha512:" && file != "" && substr(file, length(file) - length(suffix) + 1) == suffix { print file, $2; exit }
  ' | tr -d "\"'"
}

verify() {
  command -v openssl >/dev/null 2>&1 || { say "Skipping the checksum because openssl is missing."; return; }
  [ "$(openssl dgst -sha512 -binary "$1" | openssl base64 -A)" = "$2" ] \
    || fail "The download did not match its checksum. Try again."
}

launch_linux() {
  [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ] || return 0
  if command -v setsid >/dev/null 2>&1; then
    setsid "$1" >/dev/null 2>&1 &
  else
    "$1" >/dev/null 2>&1 &
  fi
}

# An AppImage cannot mount itself without the FUSE 2 library, which several distributions no longer
# install by default.
has_fuse2() {
  for directory in /lib /lib64 /usr/lib /usr/lib64 /usr/local/lib "/usr/lib/$(uname -m)-linux-gnu"; do
    if [ -e "$directory/libfuse.so.2" ]; then return 0; fi
  done
  PATH="$PATH:/sbin:/usr/sbin" ldconfig -p 2>/dev/null | grep -q "libfuse\.so\.2"
}

fuse2_command() {
  case "$(. /etc/os-release 2>/dev/null; printf '%s %s' "${ID:-}" "${ID_LIKE:-}")" in
    *arch*) echo "pacman -S --needed --noconfirm fuse2" ;;
    *ubuntu* | *debian*) echo "apt-get install -y libfuse2t64" ;;
    *fedora* | *rhel*) echo "dnf install -y fuse-libs" ;;
    *suse*) echo "zypper install -y libfuse2" ;;
  esac
}

as_root() {
  if [ "$(id -u)" = 0 ]; then "$@"
  elif command -v sudo >/dev/null 2>&1; then sudo "$@"
  else return 1
  fi
}

can_root() { [ "$(id -u)" = 0 ] || command -v sudo >/dev/null 2>&1; }

# Empty when this system has no known command.
fuse2_root_command() {
  base="$(fuse2_command)"
  [ -n "$base" ] || return 0
  if [ "$(id -u)" = 0 ]; then printf '%s' "$base"; return 0; fi
  command -v sudo >/dev/null 2>&1 && printf 'sudo %s' "$base"
  return 0
}

# sudo reads its password from the terminal, so this works when the script arrives through a pipe.
install_fuse2() {
  privileged="$(fuse2_root_command)"
  [ -n "$privileged" ] || return 1
  say "AI Coding Tool needs the FUSE 2 library. Installing it with: $privileged"
  # Word splitting is intended: every command above is fixed.
  $privileged || return 1
  has_fuse2
}

# Installed builds are Electron (the earlier app) or Go (this one). Both use the same profile, so
# threads and settings carry over either way.
mac_build() {
  if [ -d "$1/Contents/Frameworks/Electron Framework.framework" ]; then echo Electron; else echo Go; fi
}

mac_version() { /usr/libexec/PlistBuddy -c 'Print CFBundleShortVersionString' "$1/Contents/Info.plist" 2>/dev/null || true; }

mac_running() { pgrep -f "/$APP_NAME.app/Contents/MacOS/" >/dev/null 2>&1; }

# Asks the app to quit as the Dock's Quit does, so it can save its work or ask first. Then waits out
# ShipIt, which installs an Electron update the app downloaded once the app has quit.
quit_mac() {
  if mac_running; then
    say "Quitting $APP_NAME so it can be replaced."
    osascript -e "tell application id \"$BUNDLE_ID\" to quit" >/dev/null 2>&1 || true
  fi
  waited=0
  while mac_running || pgrep -x ShipIt >/dev/null 2>&1; do
    [ "$waited" -lt 60 ] || fail "$APP_NAME is still running. Quit it, then run this again."
    sleep 1
    waited=$((waited + 1))
  done
}

deb_installed() {
  command -v dpkg-query >/dev/null 2>&1 \
    && dpkg-query -W -f '${Status}' "$DEB_PACKAGE" 2>/dev/null | grep -q 'ok installed'
}

deb_build() { if [ -e "$DEB_DIR/resources/app.asar" ]; then echo Electron; else echo Go; fi; }

deb_version() { dpkg-query -W -f '${Version}' "$DEB_PACKAGE" 2>/dev/null || true; }

# Electron AppImages carry Chromium and pass 100 MB; Go ones are under half that. Their runtimes
# cannot be relied on to list their contents.
appimage_build() {
  if [ "$(wc -c < "$1" | tr -d ' ')" -gt 100000000 ]; then echo Electron; else echo Go; fi
}

linux_running() { pgrep -x aicodingtool >/dev/null 2>&1; }

# $1 is the installed build (Electron or Go), $2 its version when known.
announce() {
  if [ "$1" = Electron ]; then
    say "Replacing the Electron build of $APP_NAME${2:+ $2} with the Go build $version. Your threads and settings carry over."
  elif [ "${2:-}" = "$version" ]; then
    say "Reinstalling $APP_NAME $version."
  elif [ -n "${2:-}" ]; then
    say "Updating $APP_NAME $2 to $version."
  else
    say "Updating $APP_NAME to $version."
  fi
}

command -v curl >/dev/null 2>&1 || fail "curl is required."

system="$(uname -s)"
feed_name="$(feed_for "$system" "$(uname -m)")"
feed="$(curl -fsSL "$DOWNLOAD/$feed_name")" || fail "Could not read the latest release ($feed_name)."
version="$(printf '%s\n' "$feed" | sed -n 's/^version: *//p' | head -n 1 | tr -d "\"'")"

existing=""
if [ "$system" = Darwin ]; then
  suffix=.zip
  for directory in /Applications "$HOME/Applications"; do
    if [ -d "$directory/$APP_NAME.app" ]; then existing="$directory/$APP_NAME.app"; break; fi
  done
  if [ -n "$existing" ] && [ -w "$(dirname "$existing")" ]; then
    applications="$(dirname "$existing")"
  else
    applications=/Applications
    [ -w "$applications" ] || applications="$HOME/Applications"
  fi
else
  suffix=.AppImage
  applications="$HOME/Applications"
  # A .deb install is upgraded as a .deb, so its menu entry and command keep pointing at one app.
  if deb_installed && [ -n "$(pick .deb)" ] && can_root; then suffix=.deb; fi
fi
set -- $(pick "$suffix")
asset="${1:-}"
checksum="${2:-}"
[ -n "$asset" ] && [ -n "$checksum" ] || fail "The release feed could not be read."
mkdir -p "$applications"

# Staged inside the destination so the final move is a rename on the same filesystem.
staging="$applications/.aicodingtool-install.$$"
rm -rf "$staging"
mkdir "$staging"
trap 'rm -rf "$staging"' EXIT INT TERM

say "Downloading $APP_NAME $version for $system $(uname -m)."
curl -fL --retry 3 --progress-bar -o "$staging/$asset" "$DOWNLOAD/$asset" \
  || fail "The download did not finish."
verify "$staging/$asset" "$checksum"

if [ "$system" = Darwin ]; then
  /usr/bin/ditto -x -k "$staging/$asset" "$staging/unpacked" || fail "The download could not be unpacked."
  bundle="$(cd "$staging/unpacked" && ls -d ./*.app 2>/dev/null | head -n 1 | sed 's|^\./||')"
  [ -n "$bundle" ] || fail "The download contained no app."
  target="$applications/$bundle"
  if [ -d "$target" ]; then announce "$(mac_build "$target")" "$(mac_version "$target")"; fi
  quit_mac
  rm -rf "$target"
  mv "$staging/unpacked/$bundle" "$target"
  xattr -dr com.apple.quarantine "$target" 2>/dev/null || true
  say "Installed $target"
  open -a "$target"
elif [ "$suffix" = .deb ]; then
  announce "$(deb_build)" "$(deb_version)"
  as_root apt-get install -y --reinstall "$staging/$asset" || fail "The package could not be installed."
  say "Installed $DEB_DIR"
  if linux_running; then
    say "Quit and reopen $APP_NAME to switch to $version."
  else
    launch_linux aicodingtool
  fi
else
  target="$applications/AICodingTool.AppImage"
  if [ -f "$target" ]; then announce "$(appimage_build "$target")"; fi
  running=no
  if linux_running; then running=yes; fi
  chmod 755 "$staging/$asset"
  mv -f "$staging/$asset" "$target"
  say "Installed $target"
  if deb_installed; then
    say "The $(deb_build) .deb is still installed. Remove it with: sudo apt remove $DEB_PACKAGE"
  fi
  if ! has_fuse2 && ! install_fuse2; then
    manual="$(fuse2_root_command)"
    [ -n "$manual" ] || manual="install the FUSE 2 library"
    say "It needs FUSE 2 before it can start: $manual, then run $target once."
  elif [ "$running" = yes ]; then
    say "Quit and reopen $APP_NAME to switch to $version."
  else
    launch_linux "$target"
    say "It adds itself to your app menu the first time it runs."
  fi
fi
