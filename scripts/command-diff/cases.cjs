// Writes every command and window event, its fields each swapped for values from a pool, with whether
// the TypeScript checks take it: the window's (isWorkspaceViewInput), an agent's (isExternalCommand)
// and a phone's (isPhoneCommand). The first line is the declarations themselves. cmd/command-check
// answers the same with internal/contracts.
//   node cases.cjs <dist> > cases.jsonl   (dist holds main/contracts/workspace-view-input.js)
const path = require("node:path");
const dist = process.argv[2];
const contracts = (name) => require(path.join(dist, "main/contracts", name));
const { isWorkspaceViewInput, isExternalCommand, isPhoneCommand, workspaceCommandDefinitions, commandPlacement, commandLeaves, isAppCommandType } = contracts("workspace-view-input.js");

const long = (n) => "a".repeat(n);
const GENERIC = [
  undefined, null, "", " ", "x", "task-1", "/tmp/a", "a\u0000b", long(257), long(1001), long(4097), long(20_000),
  0, 1, -1, 2, 1.5, 6, 19, 20, 24, 168, 1e9, -0.5, true, false, [], ["x"], [""], [1], {}, { kind: "x" },
];

/** Values a field of this name can hold, beside the generic pool, so each command has one it takes. */
const NAMED = {
  role: ["coordinator", "member", "designer"],
  engine: ["claude", "codex", "opencode", "gemini"],
  effort: ["low", "high", "max", "ultra", "galaxy"],
  policy: ["confirm", "plan", "allow-edits", "autonomous", "bypass", "yolo"],
  model: ["opus", "gpt-6.1-sol", "openai/gpt-9", "constructor", "a b"],
  list: ["threads", "destinations", "other"],
  mode: ["uncommitted", "branch", "light", "dark", "system", "other"],
  step: ["targets", "base", "commit", "custom", "merge", "fix-ci", "resolve-conflicts", "create-pull-request", "other"],
  surface: ["any", "browser", "desktop", "other"],
  permission: ["accessibility", "screenRecording", "other"],
  key: ["ArrowUp", "ArrowDown", "Tab", "Enter", "Escape", "Space"],
  hours: [1, 4, 24, 72, 168, 2, 0, "1"],
  delta: [1, -1, 2, 0],
  index: [0, 3, 19, 20, -1, 1.5],
  cols: [1, 80, 0, 1000, 10000, 1.5],
  rows: [1, 24, 0, 1000, 10000, 1.5],
  commit: ["4b87279", "4b87279deadbeef4b87279deadbeef4b87279d", "abc", "zzzzzzz", "4B87279"],
  host: ["mac.tail.ts.net", "127.0.0.1:8787", "https://mac.tail.ts.net", "host:70000", "Mac.local"],
  root: ["/srv/app", "  ", "~/code"],
  computerId: ["studio", ""],
  section: ["general", "appearance", "usage", "phone", "archive", "computer-use", "threads", "priority", "snoozed", "running", "other", "devices", "engines"],
  group: ["sidebar", "working", "failed", "completed", "running", "finished", "done", "decisions", "members", "nowhere", "other"],
  theme: ["dark"],
  sourceId: ["chrome", "safari", "firefox", "arc", "chrome:Default", "other"],
  site: ["github.com", ""],
  source: ["attachment://file/a.png", "message-image://file/x.png?y=1", "attachment://other", "data:image/png;base64,AAAA", "file:///tmp/a.png", "https://x/y.png", "/tmp/a.png", "attachments/a.png", "javascript:alert(1)"],
  gallery: [["attachment://file/a.png"], ["data:image/png;base64,AAAA"], ["javascript:x"], Array.from({ length: 501 }, () => "attachment://file/a.png")],
  range: [{ kind: "branches", base: "main", compare: null }, { kind: "branches", base: "main", compare: "dev" }, { kind: "branches", base: "", compare: null }, { kind: "branches", base: "main" }, { kind: "uncommitted" }, { kind: "branch" }, { kind: "commit", commit: "4b87279" }, { kind: "since", commit: "4b87279" }, { kind: "commit", commit: "zz" }, "branch", "uncommitted"],
  target: [
    { projectId: "p", index: 0 }, { projectId: null, index: 1.5 }, { projectId: "p" },
    { type: "uncommittedChanges" }, { type: "baseBranch", branch: "main" }, { type: "commit", sha: "4b8", title: null }, { type: "commit", sha: "4b8" }, { type: "commit", sha: "4b8", title: "T" }, { type: "custom", instructions: "x" }, { type: "custom" },
    { kind: "uncommitted" }, { kind: "base", branch: "main" }, { kind: "commit", sha: "4b87279" }, { kind: "commit", commit: "4b87279", title: "t" }, { kind: "custom", instructions: "look" },
    { kind: "thread", taskId: null }, { kind: "thread", taskId: "t" }, { kind: "browser", tabId: "b" }, { kind: "terminal", terminalId: "t" }, { kind: "review", owner: "o" }, { kind: "panel", owner: "o", panel: "p" }, { kind: "panel", owner: "o" },
  ],
  destination: [{ kind: "local" }, { kind: "new" }, { kind: "worktree", id: "w" }, { kind: "worktree" }, { kind: "worktree", id: "" }],
  place: [{ project: "p", worktree: true }, { project: "p" }, { project: 1, worktree: false }],
  proposalIds: [["p1"], ["p1", "p2"], [], Array.from({ length: 30 }, (_, i) => `p${i}`)],
  point: [{ anchor: "m1", depth: 0.5 }, null, { anchor: "m1" }],
  sender: [{ threadId: "t", title: "T", computer: "c" }, { threadId: "", title: "T", computer: "c" }, { threadId: "t", computer: "c" }],
  lead: [{ threadId: "t", title: "T", computer: "c" }, { threadId: "t", title: "T", computer: "c", proposalId: "p" }, { threadId: "t", title: "T", computer: "c", proposalId: null }, { threadId: "t", title: "T", computer: "c", proposalId: "" }],
  brief: [{ intent: "do", doneWhen: "done", delivers: "report" }, { intent: "do", doneWhen: "done", delivers: "release" }, { intent: "", doneWhen: "done", delivers: "commit" }, { intent: "do" }],
  attachments: [
    [{ path: "/tmp/a.png", labels: ["a"] }], [{ path: "/tmp/a.png", labels: ["a"], context: null }], [{ path: "/tmp/a.png" }],
    [{ id: "i", source: "data:image/png;base64,AAAA", annotations: [] }], [{ id: "i", source: "data:image/png;base64,AAAA", annotations: [{ kind: "box", x: 0.5, y: 1, width: 2, height: 3, text: "t" }] }],
    [{ id: "i", source: "data:image/png;base64,AAAA", annotations: [{ kind: "circle", x: 0, y: 1, width: 2, height: 3, text: "t" }] }],
    [{ id: "i", source: "data:image/png;base64,AAAA", annotations: [], path: null }], [{ id: "i", source: "data:image/png;base64,AAAA", annotations: [], path: "/tmp/a" }],
  ],
  annotations: [
    [{ id: "a", quote: "q", note: "n" }], [{ id: "a", quote: "q", note: "n", anchor: { kind: "message", messageId: "m", start: 0, end: 1.5 } }],
    [{ id: "a", quote: "q", note: "n", anchor: { kind: "diff", comparison: "c", path: "p", start: "1", end: "2", side: "old" } }],
    [{ id: "a", quote: "q", note: "n", anchor: { kind: "diff", comparison: "c", path: "p", start: 1, end: 2, side: "old" } }], [{ id: "a", quote: "q" }], [{ id: "a", quote: "q", note: "n", anchor: null }],
  ],
  anchor: [{ kind: "message", messageId: "m", start: 0, end: 4 }, { kind: "diff", comparison: "c", path: "p", start: "1", end: "2", side: "new" }, { kind: "diff", comparison: "c", path: "p", start: "1", end: "2", side: "both" }, { kind: "message", messageId: "m", start: "0", end: 4 }],
  pastes: [[{ id: "p", text: "t" }], [{ id: "p" }], [{ id: "p", text: null }]],
  files: [[{ path: "/a", name: "a" }], [{ path: "/a", name: "a", folder: true }], [{ path: "/a", name: "a", folder: false }], [{ id: "f", path: "/a", name: "a" }], [{ id: "f", path: "/a", name: "a", folder: true }], [{ id: "f", path: "/a", name: "a", folder: null }]],
  paths: [["/a"], [1]],
  roots: [["/a"], []],
  draft: [
    { schedule: "0 8 * * *", prompt: "Check", policy: "confirm" }, { schedule: "0 8 * * *", prompt: "Check" }, { schedule: "0 8 * * *", prompt: "Check", surfaceWhen: "x", endsWhen: "y", timezone: "UTC" },
    { schedule: "", prompt: "Check" }, { schedule: "nope", prompt: "Check" }, { schedule: "0 8 * * *", prompt: "" }, { schedule: "2026-12-01T10:00:00Z", prompt: "Once" },
  ],
  patch: [{ paused: true }, { schedule: "0 9 * * *" }, { prompt: "p" }, { paused: "yes" }, {}, { policy: "bypass" }, { surfaceWhen: "" }, { endsWhen: "x" }, { timezone: "UTC" }],
  action: [
    { kind: "click", ref: "e1" }, { kind: "type", ref: "e1", text: "hi" }, { kind: "type", ref: "e1", text: "hi", submit: true }, { kind: "scroll", dy: 100 }, { kind: "back" },
    { kind: "click" }, { kind: "press", key: "Enter" }, { kind: "wait", condition: "text", value: "x" }, { kind: "wait", condition: "network-idle" }, { kind: "eval", code: "1" },
  ],
  input: [
    { kind: "tap", x: 10, y: 20 }, { kind: "swipe", x: 0, y: 0, toX: 10, toY: 10 }, { kind: "type", text: "hi" }, { kind: "button", button: "home" }, { kind: "button", button: "lock" },
    { kind: "key", key: "a" }, { kind: "paste", text: "x" }, { kind: "copy" }, { kind: "tap", x: "1", y: 2 }, { kind: "rotate", orientation: "portrait" }, { kind: "drag", points: [] },
  ],
  options: [{ sound: true, focus: false }, { sound: true }, { sound: true, focus: 1 }, { hideCursor: true }, { sound: false, hideCursor: false }, {}, { sound: "x" }],
  base: [{ schedule: "s", prompt: "p", surfaceWhen: "", endsWhen: "" }, { schedule: "s", prompt: "p" }],
  fields: [{ schedule: "s", prompt: "p", surfaceWhen: "", endsWhen: "" }, { schedule: "s" }],
  title: ["Title", "  ", long(121), long(120)],
  then: ["go on", "  ", long(100_001)],
  memory: ["remember", long(100_001)],
  text: ["hello", long(257)],
  data: ["ls\r", long(100_001)],
  id: [1, 2.5, "1"],
  size: [14, 0, -1, 13.5],
  name: ["Mac", null],
  warning: ["w"],
  quiet: [true, false],
  afterRun: [true, false],
  ifWaiting: [true, false],
  start: [true, false, 0, "1"],
  folder: [true, false],
  line: [1, 1.5, -1],
};


Object.assign(NAMED, {
  taskId: ["t-1", "", long(257)], threadId: ["t-1", ""], projectId: ["p-1", ""], runId: ["r-1"], worktreeId: ["w-1", ""], tabId: ["b-1"], terminalId: ["term-1"],
  url: ["https://example.com", "example.com", "about:blank", "javascript:alert(1)", "file:///etc/passwd", long(8193)],
  binding: ["Mod+Shift+K", "K", null], message: ["Saved", long(4097)], page: ["general", "devices", "other"], chatId: ["side-1", ""], appId: ["vscode", ""],
  enabled: [true, false], open: [true, false], all: [true, false], favorite: [true, false], zen: [true, false], focused: [true, false], expanded: [true, false],
  menu: ["session:location", "thread-start:project", null], panel: ["changes", "terminal", "other"], tab: ["browser", "home"], filter: ["all", "this", "c-1"],
  settingId: ["theme", ""], subagentId: ["s-1"], workflowId: ["wf-1"], approvalId: ["a-1"], decisionId: ["d-1"], answer: ["Left"], noteId: ["n-1"], proposalId: ["p-1"],
  family: ["aicodingtool", "nord"], font: ["system", "inter"], systemDark: [true, false], dark: [true, false], away: [true, false], muted: [true, false], purpose: ["Ship", ""],
  question: ["q"], requestId: ["q-1"], questionId: ["q"], code: ["ABCD1234", "12"], deviceId: ["dev-1"], device: ["SIM-1"], udid: ["SIM-1"], branch: ["main", "", "-x"],
  create: [true, false], worktree: [true, false, null], steer: [true, false], everywhere: [true, false], refresh: [true, false], send: [true, false], held: [true, false],
  results: [{ matches: 3 }, { matches: 3, index: 1 }, { matches: 3, counting: true }, { matches: "3" }, {}], refusal: [{ reason: "unsupported", binding: "Mod+Space", message: "m" }, { reason: "taken", binding: "b", message: "m" }],
});
for (const [field, values] of Object.entries(NAMED)) if (!values.includes(undefined)) values.push(undefined);

const definitions = workspaceCommandDefinitions();
const declared = definitions.map(({ type, fields }) => ({ type, fields, at: commandPlacement(type), leaves: commandLeaves(type) }));
console.log(JSON.stringify({ definitions: declared }));

// Field checks by command, from each feature's table; the window's events are not exported, so their bases are given.
const fs = require("node:fs");
const checks = {};
for (const file of fs.readdirSync(path.join(dist, "main/contracts"))) {
  if (!file.endsWith("-commands.js")) continue;
  for (const table of Object.values(contracts(file))) {
    if (!table || typeof table !== "object") continue;
    for (const [type, declaration] of Object.entries(table)) if (declaration && declaration.fields && declaration.at) checks[type] = declaration.fields;
  }
}
const EVENTS = [
  { type: "action.failed", fields: ["message", "page"], base: { message: "Failed" } },
  { type: "find.results", fields: ["target", "results"], base: { target: { kind: "thread", taskId: null }, results: { matches: 1 } } },
  { type: "shortcut.captured", fields: ["binding"], base: { binding: "Mod+K" } },
  { type: "shortcut.unavailable", fields: ["refusal"], base: { refusal: { reason: "unsupported", binding: "Mod+Space", message: "m" } } },
];
let seed = 7;
const random = () => { seed = (seed * 1103515245 + 12345) % 2147483648; return seed / 2147483648; };
const verdicts = (input) => ({ view: isWorkspaceViewInput(input), external: isExternalCommand(input), phone: isPhoneCommand(input) });
const pool = (field) => [...(NAMED[field] ?? []), ...GENERIC];
const missing = [];
let cases = 0;
const emit = (input, field) => { cases++; console.log(JSON.stringify({ input, ts: verdicts(input), ...(field ? { field } : {}) })); };
for (const { type, fields, base: given } of [...definitions, ...EVENTS]) {
  const base = { type, ...given };
  let complete = true;
  if (!given) for (const field of fields) {
    const takes = pool(field).filter((value) => { try { return checks[type][field](value); } catch { return false; } });
    if (!takes.length) { complete = false; missing.push(`${type}.${field}`); continue; }
    if (!takes.includes(undefined)) base[field] = takes[0];
  }
  if (!complete) continue;
  if (!isWorkspaceViewInput(base)) missing.push(`${type} (base refused: ${JSON.stringify(base).slice(0, 200)})`);
  emit(base);
  for (const field of fields) {
    for (const value of pool(field)) {
      const input = { ...base };
      if (value === undefined) delete input[field]; else input[field] = value;
      emit(input, field);
    }
  }
  emit({ ...base, unknown: 1 }, "unknown");
  // Every field set at once, each field swapped on top of that, and random mixes.
  const full = { ...base };
  if (!given) for (const field of fields) {
    const value = pool(field).find((candidate) => candidate !== undefined && (() => { try { return checks[type][field](candidate); } catch { return false; } })());
    if (value !== undefined) full[field] = value;
  }
  emit(full, "full");
  for (const field of fields) for (const value of pool(field)) {
    const input = { ...full };
    if (value === undefined) delete input[field]; else input[field] = value;
    emit(input, field);
  }
  for (let mix = 0; mix < 30; mix++) {
    const input = { type };
    for (const field of fields) {
      const values = pool(field);
      const value = values[Math.floor(random() * values.length)];
      if (value !== undefined) input[field] = value;
    }
    emit(input, "mix");
  }
}
for (const value of [null, 1, "task.new", [], {}, { type: 1 }, { type: "nope" }, { type: "constructor" }, { type: "__proto__" }, { type: "toString" }]) emit(value, "shape");
console.error(`${cases} cases`);
if (missing.length) console.error("NO SAMPLE:\n" + missing.join("\n"));

