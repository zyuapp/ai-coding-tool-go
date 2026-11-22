---
name: update-critical-deps
description: Check the Claude Agent SDK, Codex, and the Cua Driver for new releases, port and commit the ones that carry no breaking changes, and report back on the ones that do.
disable-model-invocation: true
---

# Update critical dependencies

This app ships no npm runtime for its engines. It runs the Claude Code, Codex and OpenCode commands the user installed, and talks to them through protocol code ported by hand. Three dependencies pin that code:

- **Claude Agent SDK.** `internal/engines/claude/protocol` reproduces the SDK's wire behaviour: the command line and environment it starts Claude Code with (`options.go`, `query.go`), the stream-json messages (`messages.go`), the control requests (`control.go`) and the in-process MCP servers (`mcp.go`). `SDKVersion` in `options.go` names the SDK it follows and is sent to Claude Code as `CLAUDE_AGENT_SDK_VERSION`. `BaselineVersion` in `internal/engines/claude/baseline.go` is the oldest Claude Code offered every model.
- **Codex.** `internal/engines/codex/protocol` (`types.go`, `events.go`, `app_server_client.go`) is a hand-written subset of the app-server protocol, following `Version` in `types.go`. Call sites are the rest of `internal/engines/codex`, including the private Codex home in `codex_home.go`.
- **Cua Driver.** `scripts/prepare-cua-driver.sh` pins the version and the macOS, Linux x64 and Linux arm64 archive checksums. `internal/computeruse/host.go` starts it as `serve --embedded …` and proxies runs through `mcp --embedded …`. Its notices are `assets/legal/CUA-RUST-DEPENDENCIES.html`, `assets/legal/UBJS-NATIVE-DEPENDENCIES.html`, and the CUA section of `assets/legal/THIRD-PARTY-NOTICES.txt`.

The Electron app beside this checkout, `../ai-coding-tool`, tracks the same three. Its `.claude/skills/update-critical-deps` and its git history are the reference for each move. Read them; never commit to it.

Never hand-edit a notice or invent release metadata. `~/.local/bin/cua-driver`, the standalone CLI, is a separate install and out of scope.

## 1. Check

```sh
grep -n 'SDKVersion =' internal/engines/claude/protocol/options.go
grep -n 'BaselineVersion =' internal/engines/claude/baseline.go
grep -n 'const Version' internal/engines/codex/protocol/types.go
grep -n '^version=' scripts/prepare-cua-driver.sh
npm view @anthropic-ai/claude-agent-sdk version
npm view @openai/codex version
npm view @trycua/cua-driver version
git -C ../ai-coding-tool log --oneline -5 -- package.json
```

All current: say so, stop. Ignore the `check_for_update` MCP tool; it reports the standalone CLI from a cache.

## 2. Analyse each new version for breaking changes

All three are pre-1.0, so a patch bump can still break. Read before changing anything, and work in a temp directory so the tree stays untouched while you decide.

1. Read the release notes.
   - Agent SDK: releases on `anthropics/claude-agent-sdk-typescript`.
   - Codex: the matching CLI release on `https://github.com/openai/codex/releases`.
   - Cua Driver: `https://github.com/trycua/cua/releases/tag/cua-driver-rs-v<version>`.
2. Diff the pinned version against the candidate.
   - **Agent SDK:** `npm pack @anthropic-ai/claude-agent-sdk@<pinned>` and `@<candidate>`, unpack both, and diff `sdk.d.ts` and the other `.d.ts` files. Format both `sdk.mjs` bundles (`npx prettier --parser babel`) and diff them for the command-line flags (`--…`), environment names (`CLAUDE_…`), message and control-request types, and the `initialize` handshake. Note the candidate's `claudeCodeVersion` in `package.json`.
   - **Codex:** pack `@openai/codex@<pinned>-darwin-arm64` and `@<candidate>-darwin-arm64`, run each `vendor/aarch64-apple-darwin/bin/codex app-server generate-ts --out <dir>`, and diff the two outputs. The Go types are not generated, so the diff between versions is what matters, not a diff against this repo.
   - **Cua Driver:** download the candidate archive for this machine, then run `cua-driver --version`, `cua-driver serve --help` and `cua-driver mcp --help`. Confirm every flag `host.go` passes still exists with the same meaning, and diff the MCP tool list the daemon serves against the pinned binary in `build/cua-driver`.
3. Read every hit against the call sites above. For Claude and Codex, search the Go packages for each changed JSON field, message type or flag by its wire name.

For Codex, protocol shape is not enough: the private home in `codex_home.go` shares selected config and auth with the user's home while isolating sessions, SQLite and logs. This repo has no real-binary port of the Electron app's home compatibility test, so run that test against the unpacked candidate as evidence about the binary:

```sh
(cd ../ai-coding-tool && CODEX_COMPAT_BINARY=/absolute/path/to/candidate/codex npx vitest run tests/main/codex/codex-home-compatibility.test.mts)
```

It uses temporary homes and a localhost server and never needs a real login. Also inspect the candidate for new home-relative inputs, renamed storage flags, keychain changes and login/logout changes, and compare them with `codex_home.go`. A failure, or a check you could not run, is an incomplete assessment, not a safe update.

Breaking means a field, message, flag or tool a production call site uses was removed, renamed or changed incompatibly; a default changed in a way that changes behaviour; or a new required config or permission step. A response gaining a field the Go code ignores is not breaking.

**If it breaks, stop that dependency's update.** Leave its pins unchanged and report the dependency, version, breakage, affected call sites and the migration it needs. The user decides whether to take it on.

**If it is clean, continue** one dependency at a time.

## 3. Apply

**Agent SDK.** Port every wire change the diff showed into `internal/engines/claude/protocol`, then set `SDKVersion` to the candidate. Raise `BaselineVersion` only when a ported change needs a newer Claude Code than the current baseline; then use the oldest version that has it, at most the candidate's `claudeCodeVersion`, and match `../ai-coding-tool/src/main/claude/claude-baseline.ts` when the Electron app has moved too.

**Codex.** Port the changed types and events the Go code reads or sends into `types.go` and `events.go`, and set `Version` to the candidate. Keep the subset small: add only what a call site needs.

**Cua Driver.** Set `version` and the three `sha256` values in `scripts/prepare-cua-driver.sh` from the release's own archives:

```sh
for a in darwin-arm64.tar.gz linux-x86_64-binary.tar.gz linux-arm64-binary.tar.gz; do
  curl -fsSL "https://github.com/trycua/cua/releases/download/cua-driver-rs-v<version>/cua-driver-rs-<version>-$a" | shasum -a 256
done
bash scripts/prepare-cua-driver.sh && build/cua-driver/cua-driver --version
```

Update `host.go` only if a flag changed and the change is not breaking. Take the notices from the Electron app's commit that moved it to the same version, which generates them from the locked sources:

```sh
git -C ../ai-coding-tool log --oneline -S'cua-driver-rs-v<version>' -- assets/legal
git -C ../ai-coding-tool show <commit>:assets/legal/CUA-RUST-DEPENDENCIES.html > assets/legal/CUA-RUST-DEPENDENCIES.html
git -C ../ai-coding-tool show <commit>:assets/legal/UBJS-NATIVE-DEPENDENCIES.html > assets/legal/UBJS-NATIVE-DEPENDENCIES.html
```

Carry over the CUA section of `THIRD-PARTY-NOTICES.txt` from the same commit, and check that its source tag and commit name the new version. If the Electron app has not moved to this version, stop the Cua Driver update and report that its notices are missing; do not write them by hand.

## 4. Verify

```sh
gofmt -l internal
go test ./...
(cd frontend && npm run check)
```

Then exercise each moved dependency against a real binary:

- **Claude:** `CLAUDE_SMOKE=1 CLAUDE_SMOKE_TURN=1 go test ./internal/engines/claude -run Smoke -v` (one Haiku turn), with the installed Claude Code. If the user's install is older than the new baseline, say so.
- **Codex:** `CODEX_SMOKE=1 go test ./internal/engines/codex/... -run Smoke -v`, once with the installed `codex` and once with the candidate first on `PATH` when they differ. It spends no model tokens. Report which versions ran.
- **Cua Driver:** `go test ./internal/computeruse`.

Build and run a dev app (`scripts/run.sh` with a temporary `ACT_PROFILE_DIR`), and run one real turn on every engine whose dependency moved; for the Cua Driver, one turn that uses computer use. Quit the app afterwards. Call out any check you could not run, and why.

Run `git diff --check` and inspect `git diff --name-only` before committing.

## 5. Commit

Work on `main`. Make one commit per dependency, staging only its files:

- Agent SDK: `internal/engines/claude/protocol`, `internal/engines/claude/baseline.go` if it moved, and tests the port needed. Commit as `Move the Claude protocol to Agent SDK <version>`.
- Codex: `internal/engines/codex/protocol`, any call site the port needed, and tests. Commit as `Move Codex to <version>`.
- Cua Driver: `scripts/prepare-cua-driver.sh`, `internal/computeruse/host.go` if it changed, and every changed file under `assets/legal`. Commit as `Move the Cua Driver to <version>`.

`build/cua-driver/` is gitignored. The working tree may carry unrelated edits from other threads; never stage them. Don't push unless asked.
