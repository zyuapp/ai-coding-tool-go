# AI Coding Tool (Go)

Go + Wails v2 rewrite of [AI Coding Tool](https://github.com/zyuapp/ai-coding-tool), checked out beside this repository as `../ai-coding-tool` (the source of truth for behaviour). The React renderer is reused as is; the Node main process (`src/main`, `src/host`, and the `src/application`, `src/contracts`, `src/domain` code it runs) is rewritten in Go. `../ai-coding-tool-rust` is an earlier native port of the same app; its `crates/` are a second reference for how each part maps onto a compiled language, but where it and the TypeScript disagree, the TypeScript wins.

## Layout
| path | ports | role |
|---|---|---|
| `main.go` | `src/main/main.ts`, `serve.ts` | entry: the Wails window, and the `serve` / `pair` subcommands |
| `frontend/` | `src/renderer`, `index.html` | the React UI. `frontend/src/{application,contracts,domain}` are copies of the TypeScript layers the renderer imports for view projections and types, plus the declarations of this app's own features |
| `frontend/src/renderer/bridge.ts` | `src/preload.ts` | builds `window.desktop` and `window.workspace` on the Wails runtime |
| `internal/jsonx` | | JSON conventions below: unions, tags, tri-state fields, the shared options |
| `internal/domain` | `src/domain` | types and pure functions |
| `internal/contracts` | `src/contracts` | commands, their declarations and validation, ports (Go interfaces), IPC types |
| `internal/application` | `src/application` (with `state/` and `workspace-reducer/`) | workspace state, `Reduce`, effects, view projections the host needs |
| `internal/host` | `src/host` | the runtime: owns state, runs inputs through `Reduce`, performs effects, persists, publishes patches |
| `internal/platform` | `login-path`, `agent-process`, `instance-lock`, `user-data*`, `system-memory`, `lock-awake`, `open-in-*`, notices, badge, dock | everything that differs between operating systems |
| `internal/store` | `task-database*`, `json-storage`, `attachment-store`, `message-image-store` | SQLite store, schema-compatible with the Electron app's `tasks.v3.sqlite`, read and written in the generated types (rows keep each record's JSON); profile JSON files |
| `internal/workspace` | `src/main/workspace` | git, worktrees, diffs, GitHub |
| `internal/engines/{agent,claude,codex,opencode}` | `src/main/{agent,claude,codex,opencode}`, `engines.mts` | agent sessions and their protocols |
| `internal/tools` | `src/main/tools` | MCP tool definitions and the MCP HTTP host |
| `internal/terminal` | `terminal-host`, `terminal-snapshot`, `terminal-output-buffer` | PTYs and a VT screen per terminal for snapshots |
| `internal/browser` | `browser-*` | the browser panel: native WKWebView / WebKitGTK pages over the window, cookies, sign-in import |
| `internal/devices` | simulator, dictation, glass, window screenshots, screenshot context, capture flash | native helpers (`native/`) and cgo |
| `internal/computeruse` | `computer-use-host`, `computer-bridge`, `computer-queries` | the bundled cua-driver |
| `internal/computers` | `src/main/computers`, `serve*.ts` | paired computers and headless serving |
| `internal/mobile` | `src/main/mobile` | the phone bridge, pairing, Tailscale |
| `internal/automation` | `src/main/automation` | scheduled automations |
| `internal/updates` | `updates.ts`, `update-*`, `appimage-update`, `cli-install` | app updates and the `aicodingtool` command |
| `internal/services` | `main.ts` startup, `service-desktop.ts`, `desktop-events.ts`, `serve.ts` | opens every service on a profile (store, workspaces and worktrees, engines, run host, MCP tool host, scheduler, terminals, computer use, CLI install, computer reads) for both the window and `serve`, with the desktop events and the adapters from `internal/workspace`'s own records to the generated types |
| `internal/desktop` | `window-desktop`, `runtime-desktop`, `service-desktop`, `workspace-runtime-host`, `keyboard-host`, menus, protocols | the Wails glue: implements the ports, serves the window's calls, pushes events |

Packages mirror the TypeScript layers, so imports point the same way: `domain` ← `contracts` ← `application` ← `host` ← everything under `internal/` that implements a port ← `services` ← `desktop` ← `main`. `domain`, `contracts`, `application` and `host` import no OS, Wails or cgo code. Only `engines` reaches into an engine's package; tool definitions import nothing else from the services.

Each TypeScript file `src/<layer>/<dir>/<name>.ts` maps to `internal/<layer>/<dir_><name>.go` (directories flattened into a file prefix, kebab-case to snake_case), so the port of any file is found by name. Types generated from TypeScript live in `*_types.go` (see below) beside the hand-ported logic.

## Control path
Same as the original:
- Every interaction is an `AppCommand`. Commands from outside the window (agents' MCP tools, paired phones and computers) pass validation first.
- `application.Reduce(state, input) Result` is the only writer of workspace state and returns effects describing external work.
- `host.Runtime` owns the state on one goroutine. Inputs arrive on a channel and run one at a time; effects run on their own goroutines and answer with inputs (`WorkspaceEvent`s) sent back to that channel. Nothing else reads or writes the state.
- After each change the runtime publishes patches to the window (`workspace-runtime:update`), coalescing bursts (streaming text) to at most one publish per frame.
- The window is a view: it renders state, dispatches inputs through `window.workspace.request`, and reads what it shows through `window.desktop` calls.

## State
State is immutable once published, exactly as in TypeScript: the reducer never writes into a value reachable from the previous state. A change copies the path to it.
- Structs are copied by value (`next := *state`); collections of records hold pointers (`[]*Thread`, `map[string]*X`) so unchanged records are shared between versions.
- Slices and maps are copy-on-write: `slices.Clone` / `maps.Clone` before changing one, never `append` onto a slice from the previous state (use `slices.Clip` or clone).
- A TypeScript identity check (`next.threads !== previous.threads`) becomes `jsonx.Same(a, b)` (same backing array and length, same map, same pointer).
- Go backs every zero-capacity slice with one shared address, so two empty slices built apart are the same to `jsonx.Same`, unlike two `[]` literals in TypeScript. A new list whose identity decides something (an effect, a patch) while the old one may be empty too is built with `jsonx.Fresh(n)`, which always has a backing array of its own.
- `application.Patches(previous, next)` diffs by identity first, as `workspacePatches` does, and writes the same patch shapes (`contracts.WorkspacePatch`).

## JSON
Every type that crosses a boundary (window, store, phone, paired computer, MCP, engine protocols) encodes exactly as its TypeScript counterpart. `encoding/json/v2` with `jsonx.Options()` everywhere: `jsonx.Marshal` / `jsonx.Unmarshal`, never `encoding/json`. The options sort map keys, and tolerate invalid UTF-8 and duplicate names as `JSON.parse` does.
- `number` is `float64`. Hand-written code converts where it needs an int. `unknown` is `jsontext.Value` in data (always `omitzero`) and `any` in function parameters.
- `x?: T` is `omitzero`: optional scalars and objects are pointers (`*string`, `*float64`, `*bool`, `*T`) so absent stays distinct from a zero value; optional arrays and records are a nil slice / map when absent. A required array encodes `[]` even when nil.
- `T | null` is a pointer without `omitzero` (nil is `null`); `T[] | null` and `Record<K, V> | null` are `jsonx.NullSlice[T]` / `jsonx.NullMap[K, V]`. `x?: T | null` is `jsonx.Opt[T]` (absent, null or a value).
- Arrays and records of objects hold pointers (`[]*Thread`, `map[string]*Run`); single object fields hold values.
- String-literal unions are a named `string` type with constants (`ThreadRoleReviewer`). A required single-literal field is a `jsonx.Tag[lit.X]`; an optional one is a pointer to its base type.
- Discriminated object unions are an interface with one struct per member. Each member carries its discriminator as a zero-size `jsonx.Tag[lit.X]` field (package `jsonx/lit` names every literal), so constructing a member never forgets it, and the union registers its members with `jsonx.RegisterUnion` so it decodes anywhere it appears. Code switches on the member type.
- A union nested in another (`AgentEvent = RunEvent | ThreadEvent | …`) shares its member structs, and its interface carries the outer marker too, so a `RunEvent` is an `AgentEvent`. Members that share the outer key and are told apart by a key of their own decode through the nested union (`jsonx.RegisterNested`), or are merged into one struct when they are not a declared union.
- Object unions no property tells apart (`{ revision; state } | { revision; patches }`) are one struct holding every member's fields, required only where every member requires them.
- Other unions TypeScript does not discriminate (`string | number`) get a hand-written type with `MarshalJSONTo` / `UnmarshalJSONFrom`.
- Intersections and `extends` inline their parts (`json:",inline"` embedding) when their properties do not overlap, and are flattened otherwise. `Record<K, V>` and `Map<K, V>` are `map[K]V`; `Set<T>` is `jsonx.Set[T]` (an array in JSON).

## Types from TypeScript
`go run ./tools/tsgo` reads `frontend/src/{domain,contracts,application}` and `../ai-coding-tool/src/host` with the TypeScript compiler (`tools/tsgo/extract.mjs`, on the `typescript` pinned in `tools/tsgo/package.json`, installed there on first run) and writes each file's exported types, and the local types they reach, to `internal/<layer>/<file>_types.go`, following the JSON rules above; `internal/jsonx/lit` holds the literals. Run it after pulling upstream changes; never edit `*_types.go` by hand. Exported constants and functions are hand-ported, except the constants of string-literal unions.
- Names: a declared type keeps its name (first letter upper); fields are PascalCase with Go initialisms (`taskId` → `TaskID`). An anonymous object or union is named for where it sits: parent + field (`RemoteStop.answer` → `RemoteStopAnswer`), parent + discriminator value for a union member (`WorkspaceEventSnoozesElapsed`), parent + method + param, or + `Result`, in functions. A union member written once and reached again (through `Extract`, or a nested union) is the same Go type.
- Constructing a union member: `var e application.WorkspaceEvent = application.WorkspaceEventSnoozesElapsed{At: now}`. Switching: `switch e := e.(type) { case application.WorkspaceEventSnoozesElapsed: …; case contracts.ActionFailedEvent: … }`. A union's interface is its marker methods `Is<Union>()` (with the layer in the name where two layers share it).
- Object types whose members are all functions are Go interfaces (ports). A function answering a `Promise<T>` takes a `context.Context` first and returns `(T, error)`; function-typed fields are `json:"-"`.
- Generic declarations (`SliceTransition`, `Declarations`, …) are not generated; their instantiations are, structurally, where other types use them. Classes are hand-ported.
- Types it cannot express are listed in `tools/tsgo/manual.go` and written by hand in `*_manual.go` beside the generated files.
- Features only this app has (idle compaction) declare their types in the `frontend/src` copies, so a regenerate keeps them; syncing those copies from upstream keeps them too. The differential checks leave them out: `diff.cjs` strips their state and effects, `cmd/command-check` lists their commands.
- `tools/tsgo/corpus/capture.cjs` records what the built TypeScript reducer takes and produces under random inputs into `internal/application/testdata/corpus.jsonl.gz`; `go test ./internal/application` requires every value to survive a decode into the generated types and an encode back.

## Concurrency and processes
- `context.Context` replaces `AbortSignal`; a goroutine per effect replaces a promise; a channel or `sync` primitive replaces callbacks that cross goroutines.
- Every child process starts through `platform.Command`: the program is found on the search path the user's login shell reported, and the child gets that path and the app's child variables. The app never changes its own environment.
- Per-OS code sits in files chosen by build tags (`_darwin.go`, `_linux.go`), never in scattered `runtime.GOOS` checks. A Windows port adds `_windows.go` beside them.

## Profile
A source build keeps its own profile (`AI Coding Tool Go Dev` under the user config directory). Its first launch with an empty profile copies the Electron dev profile (`AI Coding Tool Dev`), read only. The installed app takes the Electron app's name, ids and profile, so it replaces that app and reads its data in place. Both share the profile's lock, so only one opens it at a time.

## Building and checks
- `wails dev` runs the app with the Vite dev server; `wails build` writes `build/bin`. `scripts/run.sh` builds and runs a dev app with `ACT_PROFILE_DIR` / `ACT_WINDOW_SIZE=WxH`.
- `scripts/prepare-cua-driver.sh` fetches the pinned Cua Driver into `build/cua-driver`, where a source build runs computer use from.
- `scripts/linux-docker.sh` builds, tests (`go test -tags webkit2_41`), runs under Xvfb and packages the Linux app in an Ubuntu container, and installs the `.deb` in a clean one.
- Releases: `scripts/package-mac.sh` / `scripts/package-linux.sh` build the installed app (production identity, helpers, driver, notices; `.deb`, AppImage and tarball on Linux), `scripts/release-feed.sh` writes the `latest-*.yml` feed `internal/updates` reads, and `.github/workflows/release.yml` publishes them to `zyuapp/ai-coding-tool-go` releases.
- `go test ./...`; `cd frontend && npm run check` for the renderer.
- Differential checks against the TypeScript backend. They run a CommonJS build of it named by `TS_DIST` (default `/tmp/goport/tsdist`); the head of `scripts/reducer-diff/run.sh` has the command that builds one. Rebuild it after pulling upstream changes.
  - `scripts/reducer-diff/run.sh [--seeds 1-50 --steps 400 --cover]` fuzzes `application.Reduce` and the host's projections (`cmd/reduce-lines`) against the TypeScript reducer with the same random inputs, and writes the inputs of the first divergence per seed to `$TRAILS/trail-<seed>.jsonl` (`replay.cjs` replays one).
  - `scripts/reducer-diff/spec/spec.sh` runs the TypeScript app's own test suite with every reduce, projection and boundary validator also run through Go, and lists where they differ.
  - `scripts/command-diff/run.sh` compares every command's declaration and its window, agent and phone validation (`cmd/command-check`).
