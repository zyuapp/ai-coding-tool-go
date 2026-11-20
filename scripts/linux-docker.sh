#!/usr/bin/env bash
# Builds, tests, runs and packages the Linux app in a Docker container (Ubuntu 24.04, this machine's
# architecture, WebKitGTK 4.1, Xvfb + Openbox).
#
# usage: scripts/linux-docker.sh [step...]   (default: image sync build test shots package install stop)
#   image     build the act-linux image
#   sync      start the act-linux container and copy this checkout into it (/work)
#   build     the renderer, native helpers and a dev binary (wails build -tags webkit2_41)
#   test      go test ./... inside the container
#   shots     run the dev binary under Xvfb on a copy of PROFILE and screenshot it at each size
#   package   scripts/package-linux.sh: .deb, .tar.gz and .AppImage, copied to OUT/package
#   install   install the .deb in a clean Ubuntu container and launch it, then the AppImage, under Xvfb
#   shell     an interactive shell in the container
#   stop      remove the container (the image and caches stay)
#
#   PROFILE  a profile folder to copy in (default: the Electron dev profile); caches, Singleton* and
#            codex-private credentials are left out
#   SIZES    window sizes to screenshot (default "900x600 1280x800 1800x1100")
#   OUT      where screenshots, logs and packages land (default /tmp/act-linux-docker)
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
image=act-linux
container=act-linux
profile=${PROFILE:-$HOME/Library/Application Support/AI Coding Tool Dev}
sizes=${SIZES:-900x600 1280x800 1800x1100}
out=${OUT:-/tmp/act-linux-docker}
mkdir -p "$out"

inside() { docker exec -i "$container" bash -lc "$1"; }

step_image() {
  docker build -t "$image" - <<'DOCKERFILE'
FROM ubuntu:24.04
ARG GO_VERSION=1.27.4
ARG NODE_MAJOR=22
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential pkg-config git curl ca-certificates xz-utils file rsync dpkg-dev fakeroot zsh sudo \
      libgtk-3-dev libwebkit2gtk-4.1-dev \
      libgtk-3-0t64 libnotify4 libnss3 libxss1 libxtst6 xdg-utils libatspi2.0-0t64 libuuid1 libsecret-1-0 \
      libsecret-tools x11-utils \
      xvfb openbox x11-apps imagemagick xdotool dbus dbus-x11 at-spi2-core \
      gstreamer1.0-plugins-base gstreamer1.0-plugins-good gstreamer1.0-gl \
      fonts-dejavu-core fonts-noto-core fonts-noto-color-emoji libfuse2t64 \
    && rm -rf /var/lib/apt/lists/*
RUN arch=$(dpkg --print-architecture) \
    && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" | tar -C /usr/local -xz \
    && node_arch=$( [ "$arch" = amd64 ] && echo x64 || echo "$arch" ) \
    && node=$(curl -fsSL "https://nodejs.org/dist/latest-v${NODE_MAJOR}.x/SHASUMS256.txt" | grep -o "node-v[0-9.]*-linux-${node_arch}.tar.xz" | head -1) \
    && curl -fsSL "https://nodejs.org/dist/latest-v${NODE_MAJOR}.x/$node" | tar -C /usr/local --strip-components=1 -xJ \
    && tool_arch=$( [ "$arch" = amd64 ] && echo x86_64 || echo aarch64 ) \
    && tool_sha=$( [ "$arch" = amd64 ] && echo ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0 || echo f0837e7448a0c1e4e650a93bb3e85802546e60654ef287576f46c71c126a9158 ) \
    && curl -fsSL -o /usr/local/bin/appimagetool.AppImage "https://github.com/AppImage/appimagetool/releases/download/1.9.1/appimagetool-${tool_arch}.AppImage" \
    && echo "$tool_sha  /usr/local/bin/appimagetool.AppImage" | sha256sum -c - \
    && chmod 755 /usr/local/bin/appimagetool.AppImage \
    && printf '#!/bin/sh\nexec /usr/local/bin/appimagetool.AppImage --appimage-extract-and-run "$@"\n' > /usr/local/bin/appimagetool \
    && chmod 755 /usr/local/bin/appimagetool
ENV PATH=/root/go/bin:/usr/local/go/bin:$PATH
RUN go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.1
WORKDIR /work
DOCKERFILE
}

step_sync() {
  if ! docker container inspect "$container" >/dev/null 2>&1; then
    docker run -d --init --name "$container" --shm-size=1g \
      -v act-linux-gomod:/root/go/pkg/mod -v act-linux-gocache:/root/.cache/go-build \
      -v act-linux-npm:/root/.npm -v act-linux-node-modules:/work/frontend/node_modules \
      "$image" sleep infinity >/dev/null
  fi
  # The host's native/bin, node_modules and build outputs are macOS binaries; the container builds its own.
  COPYFILE_DISABLE=1 tar --no-xattrs -C "$root" -cf - --exclude=./.git --exclude=./frontend/node_modules --exclude=./frontend/dist \
      --exclude=./build/bin --exclude=./build/package --exclude=./build/cua-driver \
      --exclude=./native/bin --exclude=./tmp --exclude='*.test' --exclude='.build' . \
    | docker exec -i "$container" tar -C /work -xf - --no-same-owner
  if [ -d "$profile" ]; then
    inside 'rm -rf /profile-source && mkdir -p /profile-source'
    COPYFILE_DISABLE=1 tar --no-xattrs -C "$profile" -cf - --exclude='./Singleton*' --exclude='./Cache' --exclude='./Code Cache' \
        --exclude='./GPUCache' --exclude='./DawnGraphiteCache' --exclude='./DawnWebGPUCache' \
        --exclude='./Service Worker' --exclude='./codex-private' --exclude='./Crashpad' --exclude='./logs' . \
      | docker exec -i "$container" tar -C /profile-source -xf - --no-same-owner
  fi
}

step_build() {
  inside 'set -e; cd /work/frontend
      if ! cmp -s package-lock.json node_modules/.lock-stamp; then npm ci --no-audit --no-fund >/dev/null; cp package-lock.json node_modules/.lock-stamp; fi
      cd /work && go run ./native/build
      wails build -skipbindings -clean=false -tags webkit2_41 >/tmp/wails-build.log 2>&1 || { cat /tmp/wails-build.log; exit 1; }
      file build/bin/aicodingtool'
}

step_test() {
  inside 'cd /work && go vet -tags webkit2_41 ./... && xvfb-run -a -s "-screen 0 1280x1024x24" go test -tags webkit2_41 ./... 2>&1' | tee "$out/go-test.log"
}

# start_display N WxH: Xvfb :N at that size with Openbox; display_env N points a command at it with a session bus.
display_env() { printf 'export DISPLAY=:%s; eval $(dbus-launch --sh-syntax); ' "$1"; }
start_display() {
  stop_display
  inside "(Xvfb :$1 -screen 0 ${2}x24 -nolisten tcp >/tmp/xvfb-$1.log 2>&1 &); sleep 1; (DISPLAY=:$1 openbox >/tmp/openbox-$1.log 2>&1 &); sleep 1"
}
stop_display() { inside 'pkill -x aicodingtool; sleep 1; pkill -x openbox; pkill -x dbus-daemon; pkill -x Xvfb; sleep 0.5; true'; }

# shoot N NAME: the whole screen and the app's window alone.
shoot() {
  inside "export DISPLAY=:$1; import -window root /tmp/$2-screen.png; \
      window=\$(xdotool search --onlyvisible --class aicodingtool | tail -1); xdotool getwindowgeometry \$window; \
      import -window \$window /tmp/$2.png"
  docker cp "$container:/tmp/$2.png" "$out/$2.png"
  docker cp "$container:/tmp/$2-screen.png" "$out/$2-screen.png"
  echo "$out/$2.png"
}

step_shots() {
  local n=90
  for size in $sizes; do
    local w=${size%x*} h=${size#*x}
    n=$((n + 1))
    start_display "$n" "$((w + 40))x$((h + 80))"
    inside "rm -rf /profile && cp -a /profile-source /profile"
    inside "$(display_env $n) cd /work && ACT_PROFILE_DIR=/profile ACT_WINDOW_SIZE=$size \
        nohup build/bin/aicodingtool >/tmp/app-$size.log 2>&1 & sleep 12"
    shoot "$n" "shot-$size"
    docker cp "$container:/tmp/app-$size.log" "$out/app-$size.log"
    stop_display
  done
}

step_package() {
  inside 'cd /work && SKIP_FRONTEND=1 bash scripts/package-linux.sh /work/build/package-linux'
  rm -rf "$out/package"
  docker cp "$container:/work/build/package-linux" "$out/package"
  ls -la "$out/package"
}

# A clean Ubuntu with only what the .deb depends on, plus a display to see it in.
step_install() {
  local container=act-linux-deb
  docker rm -f "$container" >/dev/null 2>&1 || true
  docker run -d --init --name "$container" --shm-size=1g -v "$out/package:/pkg:ro" ubuntu:24.04 sleep infinity >/dev/null
  inside 'export DEBIAN_FRONTEND=noninteractive; apt-get update >/dev/null && apt-get install -y --no-install-recommends \
      /pkg/*.deb xvfb openbox imagemagick xdotool dbus-x11 >/tmp/apt.log 2>&1 || { tail -20 /tmp/apt.log; exit 1; }
      dpkg -s ai-coding-tool | grep -E "^(Status|Version|Depends)"; ls -l /usr/bin/aicodingtool; ls "/opt/AI Coding Tool" "/opt/AI Coding Tool/bin/native"'
  start_display 99 1320x880
  inside "$(display_env 99) ACT_WINDOW_SIZE=1280x800 nohup aicodingtool >/tmp/app-deb.log 2>&1 & sleep 12"
  shoot 99 shot-deb
  docker cp "$container:/tmp/app-deb.log" "$out/app-deb.log"
  stop_display
  start_display 99 1320x880
  inside "$(display_env 99) cd /tmp && cp /pkg/*.AppImage app.AppImage && chmod +x app.AppImage && ACT_WINDOW_SIZE=1280x800 \
      APPIMAGE_EXTRACT_AND_RUN=1 nohup ./app.AppImage >/tmp/app-appimage.log 2>&1 & sleep 15"
  shoot 99 shot-appimage
  docker cp "$container:/tmp/app-appimage.log" "$out/app-appimage.log"
  inside 'cat ~/.local/share/applications/com.zyuapp.aicodingtool.desktop; xdg-mime query default x-scheme-handler/aicodingtool' 
  stop_display
  docker rm -f "$container" >/dev/null
}

step_shell() { docker exec -it "$container" bash -l; }
step_stop() { docker rm -f "$container" act-linux-deb >/dev/null 2>&1 || true; }

steps=("$@")
[ ${#steps[@]} -gt 0 ] || steps=(image sync build test shots package install stop)
for step in "${steps[@]}"; do
  echo "== $step"
  "step_$step"
done
