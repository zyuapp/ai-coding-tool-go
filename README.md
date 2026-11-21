# AI Coding Tool (Go)

An extremely opinionated wrapper for Claude Code, Codex, and OpenCode, rewritten in Go and [Wails](https://wails.io) to run faster and lighter than the [Electron app](https://github.com/zyuapp/ai-coding-tool). Runs on macOS (Apple silicon) and Linux (x86_64, arm64).

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/zyuapp/ai-coding-tool-go/main/install.sh | sh
```

macOS installs to `/Applications`; Linux installs an AppImage to `~/Applications`, or upgrades the `.deb` if one is installed. It replaces the Electron app in place, keeping your threads and settings. The app updates itself. Packages (`.dmg`, `.zip`, `.deb`, `.tar.gz`, `.AppImage`) are on the [releases page](https://github.com/zyuapp/ai-coding-tool-go/releases).

## Build from source

Needs Go, Node 24 and the [Wails CLI](https://wails.io/docs/gettingstarted/installation); on Linux, also GTK 3 and WebKitGTK 4.1 development files.

```sh
npm --prefix frontend ci
scripts/run.sh
```

`ARCHITECTURE.md` describes the layout.

## License

[MIT](LICENSE)
