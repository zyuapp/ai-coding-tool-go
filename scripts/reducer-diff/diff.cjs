// Differential fuzz: drives the TypeScript reducer and application.Reduce (cmd/reduce-lines) with the
// same random inputs and reports the first divergence per seed, writing its inputs to trail-<seed>.jsonl.
// States, effects and results must be equal as JSON values (key order aside).
//   go build -o /tmp/reduce-lines ./cmd/reduce-lines
//   node diff.cjs /tmp/goport/tsdist /tmp/reduce-lines [--seeds 1-20] [--steps 300] [--cover] [--only task.] [--skip a,b]
const { spawn } = require("node:child_process");
const readline = require("node:readline");
const fs = require("node:fs");
const path = require("node:path");

const [dist, goBin, ...rest] = process.argv.slice(2);
const opt = (name, fallback) => { const i = rest.indexOf(name); return i >= 0 ? rest[i + 1] : fallback; };
const [seedFrom, seedTo] = opt("--seeds", "1-20").split("-").map(Number);
const STEPS = Number(opt("--steps", "300"));
// Every this many steps, the host's projections of the state are compared too (0 turns them off).
const VIEWS_EVERY = Number(opt("--views", "3"));
const VERBOSE = rest.includes("--verbose");
const ONLY = opt("--only", null);
const COVER = { effects: {}, running: 0, pending: 0, runEvents: 0, refused: 0, accepted: 0, resourceStopped: 0, inputs: {}, unsampled: {}, scenarios: {}, views: 0, mobilePatches: 0, windowPatches: 0 };
const SKIP = new Set((opt("--skip", "") || "").split(",").filter(Boolean));

function proc(cmd, args) {
  const child = spawn(cmd, args, { stdio: ["pipe", "pipe", "inherit"] });
  const lines = [];
  const waiters = [];
  readline.createInterface({ input: child.stdout }).on("line", (line) => {
    const waiter = waiters.shift();
    if (waiter) waiter(line); else lines.push(line);
  });
  return {
    ask(value) {
      child.stdin.write(JSON.stringify(value) + "\n");
      return new Promise((resolve) => { const line = lines.shift(); if (line !== undefined) resolve(line); else waiters.push(resolve); });
    },
    close() { child.stdin.end(); child.kill(); },
  };
}

function rng(seed) {
  let a = seed >>> 0;
  const next = () => { a |= 0; a = (a + 0x6d2b79f5) | 0; let t = Math.imul(a ^ (a >>> 15), 1 | a); t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t; return ((t ^ (t >>> 14)) >>> 0) / 4294967296; };
  return { next, int: (n) => Math.floor(next() * n), pick: (list) => list[Math.floor(next() * list.length)], chance: (p) => next() < p };
}

// Command declarations from the TS contracts.
const declarations = [];
for (const file of fs.readdirSync(path.join(dist, "main/contracts"))) {
  if (!file.endsWith("-commands.js")) continue;
  const mod = require(path.join(dist, "main/contracts", file));
  for (const value of Object.values(mod)) {
    if (!value || typeof value !== "object") continue;
    for (const [type, decl] of Object.entries(value)) if (decl && decl.fields && decl.at) declarations.push([type, decl]);
  }
}
const seen = new Set();
const commands = declarations.filter(([type]) => !seen.has(type) && seen.add(type) && (!ONLY || type.startsWith(ONLY)) && !SKIP.has(type));

const { rankedThread } = require(path.join(dist, "main/domain/thread-storage.js"));
const { loadViewPreferences } = require(path.join(dist, "main/host/view-preferences-store.js"));
const storedPreferences = (stored) => loadViewPreferences({ getItem: () => JSON.stringify(stored), setItem() {} }, 1600);
const BASE = 1_790_000_000_000;
function seedStore() {
  const project = { id: "project-1", root: "/repo", workspaceId: "workspace-1" };
  const nested = { id: "project-2", root: "/Users/someone/nested/long-project", workspaceId: "workspace-2" };
  let n = 0;
  const msg = (kind, text, at) => ({ id: `seed-${(n += 1)}`, kind, text, at });
  const task = (id, over) => rankedThread({ id, title: id, engine: "claude", executionPolicy: "confirm", messages: [], continuationStatus: "none", lastChangeSnapshot: { files: [], capturedAt: 1 }, updatedAt: 1, ...over });
  const tasks = [
    task("t-1", { title: "Fix the flaky login test", projectId: project.id, continuation: { provider: "claude", value: "claude-session" }, continuationStatus: "available", updatedAt: BASE - 1000, messages: [msg("user", "Fix the login test", BASE - 5000), msg("assistant", "Here is the fix:\n\n```ts\nx\n```", BASE - 4000)] }),
    task("t-2", { title: "مرحبا — RTL 👩‍👩‍👧", projectId: project.id, updatedAt: BASE - 2000, messages: [msg("user", "hi", BASE - 6000), msg("assistant", "| a | b |\n|---|---|\n| 1 | 2 |", BASE - 5500)] }),
    task("t-3", { title: "A".repeat(200), projectId: nested.id, updatedAt: BASE - 3000, messages: [msg("user", "long", BASE - 7000), msg("assistant", "path /x/y", BASE - 6500), msg("system", "Run failed: something broke", BASE - 6400)] }),
    task("t-4", { title: "Empty thread", projectId: nested.id, updatedAt: BASE - 4000 }),
    task("t-5", { title: "Codex thread", engine: "codex", model: "gpt-5.5", projectId: project.id, worktreeId: "wt-1", continuation: { provider: "codex", value: "codex-thread" }, continuationStatus: "available", updatedAt: BASE - 5000, messages: [msg("user", "hi", BASE - 9000), msg("assistant", "- one\n- two", BASE - 8000)] }),
    task("t-6", { title: "Archived", projectId: project.id, updatedAt: BASE - 6000, archivedAt: BASE - 100 }),
    task("t-7", { title: "Review the change", role: "reviewer", projectId: project.id, updatedAt: BASE - 7000, messages: [msg("user", "Review it", BASE - 9500)] }),
  ];
  const worktrees = [
    { id: "wt-1", name: "Feature", projectId: project.id, root: "/wt/wt-1", workspaceId: "ws-wt-1", baseCommit: "abc", createdAt: BASE - 9000, lastUsedAt: BASE - 5000 },
    { id: "wt-2", projectId: nested.id, root: "/wt/wt-2", workspaceId: "ws-wt-2", baseCommit: "def", createdAt: BASE - 8000, lastUsedAt: BASE - 8000 },
  ];
  return { version: 2, projects: [project, nested], worktrees, tasks, lastFolder: project.root };
}

function pools(state, r) {
  const remote = (state.computers?.paired ?? []).flatMap((computer) => (computer.state?.threads ?? []).map((t) => t.id));
  const threads = [...(state.threads ?? []).map((t) => t.id), ...remote.slice(0, 3)];
  const projects = (state.projects ?? []).map((p) => p.id);
  const roots = [...(state.projects ?? []).map((p) => p.root), ...(state.worktrees ?? []).map((w) => w.root), "/repo", "/elsewhere"];
  const runs = Object.values(state.activeRuns ?? {}).map((run) => run.runId);
  const pending = Object.keys(state.pendingRuns ?? {});
  const terminals = Object.values(state.docks ?? {}).flatMap((dock) => (dock.terminals ?? []).map((t) => t.id));
  const current = state.threads?.find((t) => t.id === state.currentId);
  const messages = (current?.messages ?? []).map((m) => m.id);
  const automations = (state.automations ?? []).map((a) => a.id);
  const worktrees = (state.worktrees ?? []).map((w) => w.id);
  const toasts = (state.toasts ?? []).map((t) => t.id);
  const keyed = (record) => Object.values(record ?? {}).flatMap((value) => Array.isArray(value) ? value : [value]).map((item) => item && item.id).filter(Boolean);
  const annotations = keyed(state.annotations), pastes = keyed(state.pastes), images = keyed(state.images), files = keyed(state.files);
  const tabs = Object.values(state.docks ?? {}).flatMap((dock) => (dock.browserTabs ?? []).map((t) => t.id));
  const approvals = Object.values(state.approvals ?? {}).map((a) => a.approvalId);
  const processes = Object.values(state.backgroundProcesses ?? {}).flat().map((proc) => proc.id);
  const decisions = (state.threads ?? []).flatMap((t) => (t.decisions ?? []).map((d) => d.id));
  const notes = (state.threads ?? []).flatMap((t) => (t.memory?.notes ?? []).map((n) => n.id));
  const proposals = (state.threads ?? []).flatMap((t) => (t.proposals ?? []).map((n) => n.id));
  const queued = Object.values(state.queuedMessages ?? {}).flat().map((m) => m.id);
  const subagents = Object.values(state.subagents ?? {}).flat().map((m) => m.id);
  const workflows = Object.values(state.workflows ?? {}).flat().map((m) => m.id);
  return { threads, projects, roots, runs, pending, terminals, messages, automations, worktrees, toasts, annotations, pastes, images, files, tabs, approvals, processes, decisions, notes, proposals, queued, subagents, workflows };
}

const SHORTCUT_ACTIONS = ["thread.new", "thread.new-pick-project", "composer.focus", "find.open", "find.next", "find.previous", "nav.back", "nav.forward", "page.reload", "tab.new", "terminal.focus", "tab.close",
  "dock.toggle", "dock.expand", "sidebar.toggle", "settings.toggle", "slot-1", "slot-2", "slot-9", "effort.increase", "effort.decrease", "project.start", "thread.jump", "run.allow", "run.deny", "run.cancel",
  "dictation.hold", "zen.toggle", "window.capture", "app.check-for-updates", "app.open-source-licenses", "nope"];
const GENERIC = [undefined, null, true, false, 0, 1, -1, 2, 3, 120, "", "a", "hello world", "claude", "codex", "opencode", "confirm", "auto", "plan", "threads", "destinations", "project", "general", "light", "dark", "system", "high", "low", "medium", "running", "queued", "main", "https://example.com", "x".repeat(300), [], {}];

function fieldCandidates(name, p) {
  const byName = {
    taskId: [...p.threads, "nope"], threadId: [...p.threads, "nope"], projectId: [...p.projects, "nope"], runId: [...p.runs, "run-x"],
    root: p.roots, path: p.roots, cwd: p.roots, folder: p.roots, worktreeRoot: p.roots, pendingId: p.pending, terminalId: [...p.terminals, "term-x"],
    messageId: [...p.messages, "m-x"], automationId: p.automations, worktreeId: [...p.worktrees, "wt-x"], id: [...p.toasts, ...p.automations, ...p.threads, 1, 2, "x"],
    roots: [p.roots.slice(0, 2), []], ids: [p.threads.slice(0, 2), []], taskIds: [p.threads.slice(0, 3), []],
    text: ["hello", "", "/compact", "Fix it please", "line\nline"], title: ["New title", "", "  spaced  "], query: ["", "fix", "t", "zzz"],
    model: ["claude-sonnet-4-6", "claude-opus-4-7", "gpt-5.5", "haiku", "nope"], engine: ["claude", "codex", "opencode"],
    effort: ["low", "medium", "high", "xhigh", "max"], policy: ["confirm", "auto", "plan", "bypass"], executionPolicy: ["confirm", "auto", "plan", "bypass"],
    chatId: [`chat-${(global.CHATS = (global.CHATS ?? 0) + 1)}`],
    role: ["reviewer", "implementer", "researcher", "coordinator", null], coordinationRole: ["coordinator", "member"],
    annotationId: [...p.annotations, "ann-x"], pasteId: [...p.pastes, "paste-x"], imageId: [...p.images, "img-x"], fileId: [...p.files, "file-x"],
    tabId: [...p.tabs, "tab-x"], approvalId: [...p.approvals, "approval-x"], processId: [...p.processes, "bg-0"], decisionId: [...p.decisions, "d-0"],
    noteId: [...p.notes, "n-1"], proposalId: [...p.proposals, "p-0"], proposalIds: [p.proposals.slice(0, 2), []], subagentId: [...p.subagents, "sub-0"], workflowId: [...p.workflows, "wf-0"],
    messageId: [...p.messages, ...p.queued, "m-x"], requestId: ["q-1"], questionId: ["q"], answer: ["Left", "Right", "A"],
    quote: ["the code"], note: ["Look here", ""], anchor: [{ messageId: p.messages[0] ?? "m", start: 0, end: 4 }], label: ["Screenshot"], source: ["/tmp/a.png", "https://example.com/a.png"],
    url: ["https://example.com", "example.com", "about:blank", "javascript:alert(1)"], newTab: [true, false], allow: [true, false],
    host: ["mini.local"], name: ["Mac mini", "Renamed", ""], code: ["ABCD1234", "12"], filter: ["all", "this", "c-1"], computerId: ["this", "c-1"],
    branch: ["feature/x", "main", ""], create: [true, false], worktree: [true, false], destination: [{ kind: "local" }, { kind: "worktree", id: p.worktrees[0] ?? "wt-x" }, { kind: "worktree", id: "wt-x" }, { kind: "new" }], afterRun: [true, false],
    hours: [1, 3, 24], everywhere: [true, false],
    from: [...p.threads.slice(0, 1), "Lead"], coordinatorId: [p.threads[0]].filter(Boolean),
    theme: ["aicodingtool-dark", "aicodingtool-light", "nord"], family: ["aicodingtool", "nord"], font: ["system", "inter", "jetbrains-mono"], mode: ["light", "dark", "system", "split", "unified"],
    group: ["sidebar", "working", "idle", "completed", "threads", "memory"], tab: ["browser", "terminal", "changes", "side-chat"], panel: ["changes", "terminal", "browser", "overview"],
    draft: [{ prompt: "Poll the build", schedule: "0 * * * *" }], patch: [{ paused: true }, { schedule: "*/5 * * * *" }, { prompt: "New prompt" }], prompt: ["Do the thing", ""],
    muted: [true, false], away: [true, false], purpose: ["Ship v1", ""], place: [{}, { project: p.projects[0] ?? "project-1" }, { worktree: true }],
    data: ["ls\r"], cols: [80, 120], rows: [24, 40], headline: ["Found a bug"], detail: ["It breaks"], key: ["k-1"], checked: ["the logs"],
    send: [true, false], commit: ["abc1234"], range: [{ kind: "uncommitted" }, { kind: "commit", commit: "abc" }], collapsed: [true, false], viewed: [true, false], split: [true, false], ignore: [true, false],
    fastMode: [true, false], localOnly: [true, false], ifWaiting: [true, false], message: ["Saved 1 image"], warning: ["session", "weekly"], favorite: [true, false],
    zen: [true, false], focused: [true, false], expanded: [true, false], start: [true, false], refresh: [true, false], quiet: [true, false], held: [true, false],
    step: [-1, 1, "targets", "base", "commit", "custom", "resolve-conflicts", "fix-ci", "address-comments", "merge", "create-pull-request"], delta: [-1, 1, 2], index: [0, 1, 2, 5], size: [12, 14, 16, 30], open: [true, false], enabled: [true, false],
    section: ["general", "appearance", "usage", "engines", "projects", "worktrees", "shortcuts", "computer-use", "browser", "phone", "archive", "nope"],
    target: [{ projectId: p.projects[0] ?? null, index: 0 }, { projectId: null, index: 2 }, { projectId: p.projects[1] ?? "project-2", index: 1 },
      { type: "uncommittedChanges" }, { type: "baseBranch", branch: "main" }, { type: "commit", sha: "abc1234", title: null }, { type: "custom", instructions: "Look at the tests" },
      { kind: "thread", taskId: p.threads[0] ?? null }, ...p.tabs.map((tabId) => ({ kind: "browser", tabId })), ...p.terminals.map((terminalId) => ({ kind: "terminal", terminalId })), { kind: "review", owner: p.threads[0] ?? "t-1" }, { kind: "panel", owner: p.threads[0] ?? "t-1", panel: "changes" }],
    source: ["attachment://file/tmp/a.png", "message-image://file/tmp/b.png", "/tmp/a.png", "https://example.com/a.png"], gallery: [["attachment://file/tmp/a.png", "message-image://file/tmp/b.png"], []],
    permission: ["accessibility", "screenRecording"], key: ["ArrowUp", "ArrowDown", "Tab", "Enter", "Escape", "k-1"],
    action: [{ kind: "click", ref: "e1" }, { kind: "type", ref: "e2", text: "hi", submit: true }, ...SHORTCUT_ACTIONS],
    surface: ["any", "browser", "desktop"], binding: ["Mod+Shift+K", "Mod+N", "K", null],
    input: [{ kind: "tap", x: 0.5, y: 0.5 }, { kind: "touch", phase: "down", x: 0.1, y: 0.2 }, { kind: "swipe", x: 0.1, y: 0.1, toX: 0.9, toY: 0.9 }, { kind: "button", button: "home" }, { kind: "type", text: "hi" }, { kind: "key", usage: 4, down: true }, { kind: "shortcut", usage: 6 }, { kind: "paste", text: "x" }, { kind: "rotate", orientation: "landscape-left" }, { kind: "focus", focused: true }],
    mode: ["uncommitted", "branch", "light", "dark", "system"],
    base: [{ schedule: "0 * * * *", prompt: "Poll", surfaceWhen: "", endsWhen: "" }], fields: [{ schedule: "*/5 * * * *", prompt: "Poll the build", surfaceWhen: "it fails", endsWhen: "" }],
    options: [{ sound: true, focus: false }, { sound: false, focus: true }],
    appId: ["vscode", "finder", "nope"], line: [1, 12], site: ["github.com"], sourceId: ["chrome:Default", ...p.threads.slice(0, 1), null], deviceId: ["dev-1"],
    annotations: [[{ id: "ann-r", quote: "q", note: "n" }], []], pastes: [[{ id: "paste-r", text: "pasted" }], []], paths: [["/tmp/a.png"], []],
    files: [[{ path: "/repo/a.ts", name: "a.ts" }, { path: "/repo/src", name: "src", folder: true }], [{ id: "file-r", path: "/repo/b.ts", name: "b.ts" }], []],
    attachments: [[{ path: "/tmp/a.png", labels: [] }], [{ id: "img-o", source: "data:image/png;base64,AAAA", annotations: [] }], []],
    point: [{ anchor: p.messages[0] ?? "m", depth: 0.5 }, null], list: ["threads", "destinations"], device: ["SIM-1", "SIM-2"], udid: ["SIM-1", "SIM-2"],
    brief: [{ intent: "Ship it", doneWhen: "tests pass", delivers: "commit" }], lead: [{ threadId: "remote-lead", title: "Lead", computer: "c-1" }],
    sender: [p.threads[0], p.threads[1], { threadId: "remote-lead", title: "Lead", computer: "c-1" }].filter(Boolean), memory: ["Use pnpm"],
    settingId: ["theme", "nope"], workflowId: [...p.workflows, "wf-0"], systemDark: [true, false], dark: [true, false], recover: [true, false], all: [true, false], missingOnly: [true, false],
    project: [...p.projects, "nope", null], menu: ["session:location", "model", "thread-start:project", null], annotationId: [...p.annotations, "ann-x"],
  };
  // A new side chat takes an id no thread has, as the window makes one.
  if (name === "chatId") return byName.chatId;
  return [...(byName[name] ?? []), ...GENERIC];
}

// Fields that name something the state holds, and the pool each draws from.
const HELD = { tabId: "tabs", terminalId: "terminals", worktreeId: "worktrees", approvalId: "approvals", processId: "processes", decisionId: "decisions", noteId: "notes", proposalId: "proposals",
  subagentId: "subagents", workflowId: "workflows", annotationId: "annotations", pasteId: "pastes", imageId: "images", fileId: "files", automationId: "automations", runId: "runs", pendingId: "pending" };

function randomCommand(state, r) {
  const p = pools(state, r);
  // Half the time, a command about something the state holds, so deep paths get reached.
  const held = r.chance(0.5) ? commands.filter(([, decl]) => Object.keys(decl.fields).some((field) => HELD[field] && p[HELD[field]].length)) : [];
  for (let tries = 0; tries < 20; tries++) {
    const [type, decl] = r.pick(held.length ? held : commands);
    const command = { type };
    let ok = true;
    for (const [field, check] of Object.entries(decl.fields)) {
      const candidates = fieldCandidates(field, p).filter((value) => field !== "patch" || !Array.isArray(value)).filter((value) => { try { return check(value); } catch { return false; } });
      if (!candidates.length) { ok = false; COVER.unsampled[`${type}.${field}`] = (COVER.unsampled[`${type}.${field}`] ?? 0) + 1; break; }
      const value = r.chance(0.8) && candidates.length > GENERIC.length ? candidates[r.int(candidates.length - (GENERIC.filter((g) => { try { return check(g); } catch { return false; } }).length))] : r.pick(candidates);
      if (value !== undefined) command[field] = value;
    }
    // A draft carries no id; TypeScript would keep one given in excess, which no typed port can.
    if (ok && type === "file.attach" && Array.isArray(command.files)) command.files = command.files.map(({ id, ...file }) => file);
    if (ok) return command;
  }
  return { type: "view.escape" };
}

function sendCommand(state, r) {
  const p = pools(state, r);
  const text = r.pick(["Fix the bug", "hello", "/compact", "Explain this code", "Run the tests"]);
  const base = r.chance(0.7) && p.threads.length ? { taskId: r.pick(p.threads) } : r.chance(0.5) ? { project: r.pick(p.projects) } : {};
  const extra = r.chance(0.2) ? { steer: true } : r.chance(0.2) ? { worktree: true } : r.chance(0.2) ? { engine: "codex", model: "gpt-5.5" } : {};
  return { type: "task.send", ...base, text, ...extra };
}

// Answers the host would give to one effect.
function effectAnswers(state, r, effect) {
  const options = [];
  if (effect.type === "resolve-run-workspace") {
    const workspace = effect.workspace ?? { id: "workspace-1", kind: "project", root: effect.root ?? "/repo" };
    for (let i = 0; i < 4; i++) options.push(() => ({ type: "run.resolved", pendingId: effect.pendingId, workspace }));
    options.push(() => ({ type: "run.unresolved", pendingId: effect.pendingId, message: "No folder" }));
  }
  if (effect.type === "start-run") {
    const base = { taskId: effect.command.taskId, runId: effect.command.runId, sequence: 1 };
    for (let i = 0; i < 3; i++) options.push(() => ({ type: "run.event", event: { ...base, type: "run.status", status: "running" } }));
    options.push(() => ({ type: "run.event", event: { ...base, type: "run.started" } }));
  }
  if (effect.type === "create-worktree") {
    const id = `wt-${effect.taskId}`;
    const worktree = { id, root: `/wt/${id}`, workspaceId: `ws-${id}`, baseCommit: "abc", createdAt: BASE, lastUsedAt: BASE };
    for (let i = 0; i < 3; i++) options.push(() => ({ type: "worktree.created", taskId: effect.taskId, worktree, ...(effect.move ? { move: true } : {}), ...(effect.projectId ? { projectId: effect.projectId } : {}), ...(effect.then ? { then: effect.then } : {}) }));
    options.push(() => ({ type: "worktree.failed", taskId: effect.taskId, message: "git said no" }));
  }
  if (effect.type === "release-worktree" || effect.type === "delete-worktree") {
    const snapshot = r.chance(0.5) ? { commit: "abcdef1234", shortCommit: "abcdef1", ref: "refs/aicodingtool/x" } : { commit: null, shortCommit: null, ref: null };
    if (effect.taskId) options.push(() => ({ type: "worktree.released", taskId: effect.taskId, snapshot }), () => ({ type: "worktree.release-failed", taskId: effect.taskId, message: "busy" }));
    if (effect.worktreeId) options.push(() => ({ type: "worktree.deleted", worktreeId: effect.worktreeId, root: effect.root, snapshot, ...(effect.missingOnly ? { missingOnly: true } : {}) }));
  }
  if (effect.type === "list-worktrees") options.push(() => ({ type: "worktrees.loaded", worktrees: (state.worktrees ?? []).map((w) => ({ id: w.id, root: w.root, repository: "/repo", branch: r.chance(0.5) ? "main" : null, status: { changedFiles: r.int(3), comparison: null } })) }));
  if (effect.type === "checkout-branch") options.push(() => ({ type: "checkout.finished", ...(r.chance(0.3) ? { message: "conflict" } : {}) }));
  if (effect.type === "load-thread") options.push(() => ({ type: "store.thread-loaded", taskId: effect.taskId, messages: [] }));
  if (effect.type === "suggest-title") options.push(() => ({ type: "title.suggested", taskId: effect.taskId, title: "Suggested" }));
  return options;
}

function serviceAnswers(state, r, effect) {
  const options = [];
  switch (effect.type) {
    case "read-plan-usage": options.push(() => ({ type: "usage.reported", engine: effect.engine, read: effect.read, usage: r.pick([{ status: "available", subscription: r.pick(["max", null]), windows: [{ id: "five_hour", label: "Session", utilization: r.pick([null, 12, 97]), resetsAt: r.pick([null, "2026-11-15T10:00:00Z"]), period: "session" }] }, { status: "not-applicable" }, { status: "unavailable", message: "No plan" }]) })); break;
    case "read-diff": options.push(() => ({ type: "diff.loaded", owner: effect.owner, workspaceId: effect.workspaceId, range: effect.range, result: r.pick([{ status: "available", range: effect.range, ignoreWhitespace: effect.ignoreWhitespace, files: [{ path: "a.ts", status: "modified", additions: 3, deletions: 1, binary: false }], additions: 3, deletions: 1 }, { status: "error", message: "bad" }, { status: "unavailable", reason: "missing" }]) })); break;
    case "read-pull-request": options.push(() => ({ type: "pull-request.answered", workspaceId: effect.workspaceId, branch: effect.branch, read: effect.read, ...(effect.members ? { members: true } : {}), answer: r.pick([{ status: "found", pullRequest: { number: 7, title: "Fix", url: "https://github.com/o/r/pull/7", state: r.pick(["open", "draft", "merged", "closed"]), ...(r.chance(0.5) ? { checks: r.pick(["passing", "failing", "pending"]) } : {}) } }, { status: "none" }, { status: "gh-missing" }]) })); break;
    case "refresh-environment": options.push(() => ({ type: "environment.updated", workspaceId: effect.workspaceId, ...(effect.taskId ? { taskId: effect.taskId } : {}), ...(effect.runId ? { runId: effect.runId } : {}), result: ENV(r) })); break;
    case "simulator.list": options.push(() => ({ type: "simulator.listed", availability: r.pick([{ status: "available" }, { status: "unsupported", reason: "No Xcode" }]), devices: [{ udid: "SIM-1", name: "iPhone 17", runtime: "iOS 27", state: r.pick(["booted", "shutdown"]) }, { udid: "SIM-2", name: "iPad", runtime: "iOS 27", state: "shutdown" }] })); break;
    case "project-add.directories": options.push(() => ({ type: "project.directories", request: effect.request, directories: ["/repo/a", "/repo/b"], ...(r.chance(0.2) ? { error: "No such folder" } : {}) })); break;
    case "project-add.pick": options.push(() => ({ type: "project.path-picked", request: effect.request, ...(r.chance(0.7) ? { root: "/picked" } : {}), ...(r.chance(0.1) ? { error: "Cancelled" } : {}) })); break;
    case "terminal.start": options.push(() => ({ type: "terminal.updated", update: { terminalId: effect.terminalId, ...(r.chance(0.5) ? { title: "zsh" } : {}), ...(r.chance(0.5) ? { status: r.pick(["running", "exited"]) } : {}), ...(r.chance(0.2) ? { exitCode: 1 } : {}) } })); break;
    case "dictation.start": options.push(() => ({ type: "dictation.started", id: effect.id }), () => ({ type: "dictation.failed", id: effect.id, message: "No microphone" })); break;
    case "dictation.stop": options.push(() => ({ type: "dictation.transcribed", id: effect.id, text: r.pick(["hello there", "", "fix the bug"]) })); break;
    case "dictation.download": options.push(() => ({ type: "dictation.model", model: r.pick(["downloading", "ready", "failed"]), ...(r.chance(0.5) ? { progress: r.next() } : {}) })); break;
    case "browser.open": case "browser.navigate": case "browser.reload": options.push(() => ({ type: "browser.updated", page: { tabId: effect.tabId, ...(r.chance(0.7) ? { url: effect.url ?? "https://example.com/next" } : {}), ...(r.chance(0.5) ? { title: "Example" } : {}), loading: r.chance(0.5), canGoBack: r.chance(0.5), canGoForward: false, ...(r.chance(0.1) ? { error: "net::ERR" } : {}) } })); break;
    case "find-in-page": options.push(() => ({ type: "find.results", target: { kind: "browser", tabId: effect.tabId }, results: { matches: r.int(5), ...(r.chance(0.5) ? { index: r.int(3) } : {}) } })); break;
    case "find-in-terminal": options.push(() => ({ type: "find.results", target: { kind: "terminal", terminalId: effect.terminalId }, results: { matches: r.int(5) } })); break;
    case "browser-import.list-sources": options.push(() => ({ type: "browser-import.sources", sources: [{ id: "chrome:Default", browser: "Chrome", profile: "Default" }] })); break;
    case "browser-import.list-sites": options.push(() => ({ type: "browser-import.sites", sourceId: effect.sourceId, sites: [{ domain: "github.com", cookies: 12 }] })); break;
    case "browser-import.import": options.push(() => ({ type: "browser-import.done", sourceId: effect.sourceId, sites: effect.sites, result: { imported: 3, skipped: 1 } }), () => ({ type: "browser-import.failed", message: "Keychain refused" })); break;
    case "cli.read": case "cli.install": case "cli.uninstall": options.push(() => ({ type: "cli.read-status", status: { state: r.pick(["installed", "missing", "conflict"]), path: "/usr/local/bin/aic", onPath: r.chance(0.8), current: r.chance(0.8) } }), () => ({ type: "cli.failed", message: "Permission denied" })); break;
    case "computer-use.read": case "computer-use.enable": options.push(() => ({ type: "computer-use.permissions", permissions: { accessibility: r.chance(0.5), screenRecording: r.chance(0.5) }, ...(effect.type === "computer-use.enable" ? { enabling: true } : {}) }), () => ({ type: "computer-use.failed", message: "Helper missing", ...(effect.type === "computer-use.enable" ? { enabling: true } : {}) })); break;
    case "engine.read": case "engine.reload-settings": options.push(() => ({ type: "engine.status", status: Object.fromEntries(ENGINES.filter(() => r.chance(0.8)).map((engine) => [engine, READINESS(r)])) }), () => ({ type: "engine.failed", message: "Engine check failed" })); break;
    case "engine.update": options.push(() => ({ type: "engine.updated", engine: effect.engine, status: { [effect.engine]: READINESS(r) } }), () => ({ type: "engine.update-failed", engine: effect.engine, message: "npm failed" })); break;
    case "app.list": options.push(() => ({ type: "apps.listed", apps: [{ id: "vscode", label: "VS Code", kind: "editor", icon: null }, { id: "finder", label: "Finder", kind: "files", icon: null }] })); break;
    case "app.check-for-updates": options.push(() => ({ type: "app.update-changed", update: r.pick([{ status: "idle" }, { status: "available", version: "0.11.0" }, { status: "downloading", version: "0.11.0", percent: 40 }, { status: "ready", version: "0.11.0" }]) })); break;
    case "capture-shortcut": if (effect.capturing) options.push(() => ({ type: "shortcut.captured", binding: r.pick(["Cmd+Shift+K", null]) }), () => ({ type: "shortcut.unavailable", refusal: { binding: "Cmd+Space", reason: "unsupported", message: "Taken by the system" } })); break;
    case "computer.discover": options.push(() => ({ type: "computers.found", found: [{ host: "mini.local", name: "Mac mini", os: "macOS" }] }), () => ({ type: "computers.search-failed", message: "No network" })); break;
    case "computer.pair": options.push(() => ({ type: "computers.changed", name: "This Mac", links: [{ id: "c-1", name: effect.name, host: effect.host, status: "connected", error: null, pairedAt: BASE }] }), () => ({ type: "computers.pair-failed", message: "Wrong code" })); break;
    case "remote.set-enabled": case "remote.refresh": case "remote.create-pairing-code": case "remote.revoke-device": options.push(() => ({ type: "remote.changed", remote: MOBILE(r) })); break;
  }
  return options;
}

// Answers to the effects the answers above leave out.
function moreAnswers(state, r, effect) {
  const options = [];
  const record = (root) => ({ id: `ws-${root}`, kind: "project", root });
  switch (effect.type) {
    case "add-project": options.push(() => ({ type: "project.added", workspace: record(effect.root), ...(effect.request !== undefined ? { request: effect.request } : {}), ...(effect.start ? { start: true } : {}) }), () => ({ type: "project.add-finished", ...(effect.request !== undefined ? { request: effect.request } : {}), ...(r.chance(0.7) ? { error: "Not a folder" } : {}) })); break;
    case "register-project": options.push(() => ({ type: "project.registered", projectId: effect.projectId, workspace: record(effect.root) }), () => ({ type: "project.register-failed", projectId: effect.projectId, message: "No such folder" })); break;
    case "pick-project": options.push(() => ({ type: "project.opened", workspace: record(r.pick(["/picked", "/repo", "/other"])), ...(r.chance(0.3) ? { unlisted: true } : {}) })); break;
    case "list-worktrees": options.push(() => ({ type: "worktrees.failed", message: "git failed", ...(r.chance(0.5) ? { root: "/repo" } : {}) })); break;
    case "simulator.boot": options.push(() => ({ type: "simulator.booted", udid: effect.udid }), () => ({ type: "simulator.failed", udid: effect.udid, message: "Boot failed" })); break;
    case "simulator.input": options.push(() => ({ type: "simulator.turned", udid: effect.udid, orientation: r.pick(["portrait", "landscape-left", "landscape-right", "portrait-upside-down"]) }), () => ({ type: "simulator.refused", message: "Not booted" }), () => ({ type: "simulator.idle", seq: effect.seq ?? 0 })); break;
    case "engine.reload-settings": options.push(() => ({ type: "engine.settings-reload-status", status: r.pick(["pending", "reloaded", "failed"]), ...(r.chance(0.5) ? { message: "Bad settings" } : {}) })); break;
    case "computer.forward":
      if (effect.draft) options.push(() => ({ type: "computers.forwarded", draft: effect.draft }));
      for (const input of effect.inputs ?? []) if (input.taskId) options.push(() => ({ type: "computers.answered", taskId: input.taskId }));
      if (effect.projectAddRequest !== undefined) options.push(() => ({ type: "project.add-finished", request: effect.projectAddRequest, ...(r.chance(0.5) ? { error: "Remote said no" } : {}) }));
      if (effect.proposal) options.push(() => ({ type: "proposal.settled", taskId: effect.proposal.taskId, proposalId: effect.proposal.proposalId, ...(r.chance(0.6) ? { threadId: "remote-t" } : { message: "No", refused: true }) }));
      break;
    case "send-attachments": options.push(() => ({ type: "attachments.saved", ...(effect.taskId ? { taskId: effect.taskId } : {}), ...(effect.steer ? { steer: true } : {}), ...(effect.text ? { text: effect.text } : {}), ids: (effect.attachments ?? []).map((a) => a.id), attachments: (effect.attachments ?? []).map((a) => ({ path: `/tmp/${a.id}.png`, labels: [] })) }), () => ({ type: "attachments.failed", ...(effect.taskId ? { taskId: effect.taskId } : {}), message: "Too large" })); break;
    case "browser.act": case "browser.history": options.push(() => ({ type: "browser.updated", page: { tabId: effect.tabId, url: "https://example.com/acted", loading: false, canGoBack: true, canGoForward: r.chance(0.5) } })); break;
    case "find-in-page": case "find-in-terminal": options.push(() => ({ type: "find.results", target: effect.tabId ? { kind: "browser", tabId: effect.tabId } : { kind: "terminal", terminalId: effect.terminalId }, results: { matches: 0 } })); break;
  }
  return options;
}

// A paired computer's workspace: this one's, with every thread id renamed so none collides with ours.
function remoteState(state) {
  let text = JSON.stringify({ ...state, computers: { ...state.computers, paired: [] } });
  for (const thread of state.threads ?? []) text = text.split(JSON.stringify(thread.id)).join(JSON.stringify(`remote-${thread.id}`));
  return JSON.parse(text);
}

// Inputs the host pushes that answer no effect.
function extraEvents(state, r) {
  const options = [];
  options.push(() => ({ type: "store.failed", message: "Disk full" }));
  if (r.chance(0.2)) options.push(() => ({ type: "store.absent" }));
  options.push(() => ({ type: "engine.settings-reload-status", status: r.pick(["pending", "reloaded", "failed"]) }));
  options.push(() => ({ type: "worktrees.failed", message: "git failed" }));
  options.push(() => ({ type: "simulator.refused", message: "No simulator" }));
  options.push(() => ({ type: "shortcut.unavailable", refusal: { binding: "Mod+Space", reason: "unsupported", message: "Taken by the system" } }));
  options.push(() => ({ type: "shortcut.captured", binding: r.pick(["Mod+Shift+K", null]) }));
  options.push(() => ({ type: "find.results", target: r.pick([{ kind: "thread", taskId: state.currentId ?? null }, { kind: "review", owner: state.currentId ?? "t-1" }]), results: { matches: r.int(4), ...(r.chance(0.5) ? { index: 0 } : {}) } }));
  for (const computer of state.computers?.paired ?? []) {
    options.push(() => ({ type: "computer.state", id: computer.id, state: remoteState(state) }));
    options.push(() => ({ type: "computer.state", id: computer.id, state: remoteState(state) }));
  }
  options.push(() => ({ type: "computers.changed", name: "This Mac", links: [{ id: "c-1", name: "Mac mini", host: "mini.local", status: r.pick(["connected", "connected", "offline"]), error: null, pairedAt: BASE }] }));
  const agent = [];
  for (let tries = 0; tries < 8 && agent.length < 4; tries++) {
    const input = randomEvent(state, r, [], true);
    if (input.type === "run.event" || input.type === "thread.event") agent.push(input.event);
    if (input.type === "engine.settings-reload-status") agent.push(input);
  }
  if (agent.length) options.push(() => ({ type: "agent.events", events: agent }));
  return options;
}

function randomEvent(state, r, effects, plain = false) {
  const p = pools(state, r);
  const runs = Object.values(state.activeRuns ?? {});
  const options = [];
  for (const effect of effects) options.push(...effectAnswers(state, r, effect));
  for (const run of runs) for (let weight = 0; weight < 3; weight++) {
    const base = { taskId: run.taskId, runId: run.runId, sequence: (run.sequence ?? 0) + 1 };
    const ev = (event) => () => ({ type: "run.event", event: { ...base, ...event } });
    options.push(ev({ type: "run.status", status: "running" }));
    options.push(ev({ type: "run.status", status: r.pick(["succeeded", "failed", "cancelled", "awaiting-approval"]), ...(r.chance(0.3) ? { message: "Engine exited" } : {}) }));
    options.push(ev({ type: "assistant.delta", messageId: `reply-${run.runId}`, text: r.pick(["Hello ", "world.\n\n", "```ts\nx\n```\n\n"]), append: true }));
    options.push(ev({ type: "assistant.tail", messageId: `reply-${run.runId}`, text: "partial" }));
    options.push(ev({ type: "approval.requested", approvalId: `approval-${run.runId}`, title: "Run a command", description: "rm -rf x", intent: { toolId: `tool-${run.runId}`, name: "Bash", input: { command: "rm -rf x" } } }));
    options.push(ev({ type: "tool.intent", intent: { toolId: `tool-${r.int(3)}`, name: "Read", input: { file_path: "/repo/a.ts" } } }));
    options.push(ev({ type: "context.usage", tokens: 1000 + r.int(5000), limit: 200000, model: "claude-sonnet-4-6" }));
    options.push(ev({ type: "continuation.updated", continuation: { provider: "claude", value: "s-1" } }));
    options.push(ev({ type: "run.started" }));
    options.push(ev({ type: "question.answered", requestId: "q-1", questionId: "q", text: "A" }));
    options.push(ev({ type: "question.closed", requestId: "q-1" }));
    options.push(ev({ type: "context.spent", tokens: 300 }));
    options.push(ev({ type: "context.compaction-status", compacting: r.chance(0.5) }));
    options.push(ev({ type: "context.compacted", trigger: "auto", preTokens: 9000, postTokens: 2000 }));
    options.push(ev({ type: "continuation.lost" }));
    options.push(ev({ type: "computer-use.setup-required" }));
    const queued = (state.queuedMessages?.[run.taskId] ?? [])[0];
    if (queued) options.push(ev({ type: "queued.delivered", messageId: queued.id }), ev({ type: "queued.steer-failed", messageId: queued.id, message: "No" }));
    options.push(ev({ type: "run.status", status: "failed", message: "You've hit your limit", limit: { window: "session", resetsAt: (state.__now ?? BASE) + 3600000 } }));
    options.push(ev({ type: "question.requested", requestId: "q-1", request: { blocking: r.chance(0.5), questions: [{ id: "q", header: "Pick", question: "Which?", options: [{ label: "A", description: "first" }, { label: "B", description: "" }] }] } }));
  }
  options.push(() => ({ type: "snoozes.elapsed", at: BASE + r.int(4_000_000) }));
  options.push(() => ({ type: "limits.elapsed", at: BASE + r.int(8_000_000) }));
  if (p.threads.length) {
    const taskId = r.pick(p.threads);
    options.push(() => ({ type: "coordination.reported", taskId, state: r.pick(["working", "blocked", "done", "failed"]), summary: "Did the thing" }));
    options.push(() => ({ type: "coordination.decision-raised", taskId, request: { question: "Which way?", options: [{ label: "Left" }, { label: "Right", recommended: true }] }, ...(r.chance(0.5) ? { decisionId: `d-${r.int(3)}` } : {}) }));
    options.push(() => ({ type: "coordination.remembered", taskId, change: r.pick([{ kind: "purpose", purpose: "Ship it" }, { kind: "remember", text: "Use pnpm" }, { kind: "forget", noteId: "n-1" }]) }));
    options.push(() => ({ type: "coordination.proposed", taskId, proposals: [{ id: `p-${r.int(3)}`, title: "Write tests", prompt: "Write the tests", proposedAt: BASE, place: r.pick([{}, { project: "project-1" }, { worktree: true }]) }] }));
    options.push(() => ({ type: "automation.fired", fire: { automationId: `automation-${taskId}`, taskId, runId: `auto-run-${r.int(1e6)}`, prompt: "Poll", runNumber: 1 + r.int(3), ...(r.chance(0.5) ? { quiet: true } : {}), ...(r.chance(0.5) ? { unattended: true } : {}) } }));
    options.push(() => ({ type: "automations.changed", automations: p.threads.slice(0, 1 + r.int(2)).map((id) => ({ id: `automation-${id}`, taskId: id, prompt: "Poll", schedule: "0 * * * *", paused: r.chance(0.3), createdAt: BASE, updatedAt: BASE, runCount: r.int(3), nextRunAt: r.chance(0.5) ? BASE + 3_600_000 : null })) }));
    options.push(() => ({ type: "task.result-collected", taskId }));
  }
  options.push(() => ({ type: "store.persisted" }));
  options.push(() => ({ type: "view.closed" }));
  if (p.threads.length) options.push(() => ({ type: "store.thread-loaded", taskId: r.pick(p.threads), messages: [{ id: `l-${r.int(9)}`, kind: "user", text: "loaded", at: BASE }] }));
  if (p.threads.length) options.push(() => ({ type: "title.suggested", taskId: r.pick(p.threads), title: "Suggested title" }));
  if (p.threads.length) options.push(() => ({ type: "thread.event", event: { type: "goal.changed", taskId: r.pick(p.threads), goal: null } }));
  if (p.threads.length) {
    const thread = (state.threads ?? []).find((t) => t.id === r.pick(p.threads));
    const at = thread?.resourceStop && r.chance(0.6) ? thread.resourceStop.at : (state.__now ?? BASE) + r.int(1e6);
    for (let i = 0; i < 2; i++) options.push(() => ({ type: "thread.event", event: { type: "resource.stopped", taskId: thread?.id ?? "nope", stop: { at, bytes: 3 * 1024 ** 3, cleanup: r.pick(["stopping", "complete", "incomplete"]) } } }));
    if (thread?.resourceStop) options.push(() => ({ type: "resource.resume", taskId: thread.id }));
  }
  options.push(() => ({ type: "preferences.loaded", preferences: storedPreferences({ memoryProtection: r.chance(0.5), notifications: r.chance(0.8), sidebarOpen: r.chance(0.8), sessionPanelOpen: r.chance(0.5) }) }));
  if (plain) return r.pick(options)();
  if (r.chance(0.35)) options.splice(0, options.length, ...serviceEvents(state, r, effects));
  else if (r.chance(0.2)) options.splice(0, options.length, ...extraEvents(state, r));
  return r.pick(options)();
}


// Answers to the effects of the host's other services, and what those services report unprompted.
const ENGINES = ["claude", "codex", "opencode"];
const READINESS = (r) => ({ access: r.pick(["ready", "ready", "signed-out", "missing", "outdated", "unavailable"]), version: "1.0.0", ...(r.chance(0.3) ? { latest: "1.1.0" } : {}), ...(r.chance(0.2) ? { required: "1.0.5" } : {}), ...(r.chance(0.5) ? { fix: "npm i -g engine" } : {}), ...(r.chance(0.2) ? { models: ["claude-sonnet-4-6", "gpt-5.5"] } : {}) });
const ENV = (r) => r.pick([
  { status: "available", files: r.chance(0.5) ? ["a.ts", "b.ts"] : [], branch: r.pick(["main", "feature", null]), baseline: r.pick(["abc", null]), additions: r.int(40), deletions: r.int(9) },
  { status: "unavailable", reason: r.pick(["missing", "not-directory", "inaccessible", "changed"]) },
  { status: "error", message: "git failed" },
]);
const MOBILE = (r) => ({ enabled: r.chance(0.5), status: r.pick(["off", "starting", "listening", "error"]), port: r.pick([null, 8787]), addresses: [{ kind: "loopback", host: "127.0.0.1", port: 8787 }], primary: r.chance(0.5) ? { kind: "loopback", host: "127.0.0.1", port: 8787 } : null, devices: r.chance(0.5) ? [{ id: "dev-1", kind: "phone", name: "iPhone", pairedAt: BASE, lastSeenAt: r.pick([BASE, null]) }] : [], sessions: [], tailscale: { status: r.pick(["unknown", "missing", "ready"]), magicDnsName: null, certs: false, serving: false, error: null }, pairing: r.chance(0.3) ? { code: "ABCD1234", expiresAt: BASE + 600000, address: { kind: "loopback", host: "127.0.0.1", port: 8787 }, url: "aicodingtool://pair?x" } : null, error: r.chance(0.2) ? "Port busy" : null });
function serviceEvents(state, r, effects) {
  const options = [];
  const p = pools(state, r);
  for (const effect of effects) options.push(...serviceAnswers(state, r, effect));
  if (p.threads.length) {
    const taskId = r.pick(p.threads);
    const sid = `sub-${r.int(3)}`;
    const wid = `wf-${r.int(2)}`;
    const thread = (body) => () => ({ type: "thread.event", event: { taskId, ...body } });
    options.push(
      thread({ type: "subagent.started", id: sid, description: "Explore the code", ...(r.chance(0.5) ? { agentType: "Explore" } : {}), ...(r.chance(0.3) ? { sessionScoped: true } : {}), ...(r.chance(0.5) ? { model: "claude-haiku-4-5", effort: "low", prompt: "Look around" } : {}) }),
      thread({ type: "subagent.metadata", id: sid, model: "claude-haiku-4-5" }),
      thread({ type: "subagent.status", id: sid, status: r.pick(["working", "idle"]), ...(r.chance(0.5) ? { summary: "Reading files" } : {}) }),
      thread({ type: "subagent.progress", id: sid, description: "Explore the code", lastToolName: "Read", totalTokens: r.int(9000) }),
      thread({ type: "subagent.activity", id: sid, activityId: `act-${r.int(4)}`, kind: r.pick(["text", "tool"]), text: "Read a.ts" }),
      thread({ type: "subagent.finished", id: sid, status: r.pick(["completed", "failed", "stopped"]), summary: "Done" }),
      thread({ type: "workflow.started", id: wid, name: "Fan out", description: "Many agents" }),
      thread({ type: "workflow.progress", id: wid, phases: [{ index: 0, title: "Plan" }], agents: [{ index: 0, label: "agent", state: r.pick(["queued", "running", "done", "error", "stopped"]) }], totalTokens: r.int(9999), totalToolCalls: r.int(30) }),
      thread({ type: "workflow.finished", id: wid, status: r.pick(["completed", "failed", "stopped"]), summary: "Finished" }),
      thread({ type: "background.changed", processes: r.chance(0.3) ? [] : [{ id: `bg-${r.int(2)}`, kind: r.pick(["shell", "monitor"]), description: "npm run dev", ...(r.chance(0.2) ? { stopping: true } : {}) }] }),
      thread({ type: "goal.changed", goal: r.chance(0.5) ? null : { objective: "Ship it", status: r.pick(["active", "blocked"]), iterations: r.int(4) } }),
    );
    options.push(() => ({ type: "subagent.activity.loaded", taskId, subagentId: sid, activity: [{ id: "a-1", kind: "text", text: "hello", at: BASE }] }));
    options.push(() => ({ type: "computer.notice", id: "c-1", notice: { taskId, title: "Thread", headline: "Done" } }));
    options.push(() => ({ type: "proposal.settled", taskId, proposalId: `p-${r.int(3)}`, ...(r.chance(0.5) ? { threadId: "remote-t" } : {}), ...(r.chance(0.3) ? { message: "No" } : {}), ...(r.chance(0.2) ? { refused: true } : {}) }));
  }
  for (const run of Object.values(state.activeRuns ?? {})) options.push(() => ({ type: "run.event", event: { taskId: run.taskId, runId: run.runId, sequence: (run.sequence ?? 0) + 1, type: "run.retrying", message: "Overloaded", attempt: 1 + r.int(3), maxRetries: 5 } }));
  options.push(() => ({ type: "app.launched", version: r.pick(["0.10.31", "0.10.30"]) }));
  options.push(() => ({ type: "app.build", build: r.pick([null, { version: "0.10.31", selfUpdating: r.chance(0.5) }]) }));
  options.push(() => ({ type: "action.failed", message: "Something failed", ...(r.chance(0.5) ? { page: r.pick(["general", "phone"]) } : {}) }));
  options.push(() => ({ type: "engine.status", status: Object.fromEntries(ENGINES.map((engine) => [engine, READINESS(r)])) }));
  options.push(() => ({ type: "remote.changed", remote: MOBILE(r) }));
  options.push(() => ({ type: "computers.changed", name: "This Mac", links: r.chance(0.5) ? [] : [{ id: "c-1", name: "Mac mini", host: "mini.local", status: r.pick(["connecting", "connected", "offline"]), error: null, pairedAt: BASE }] }));
  if (p.threads.length) options.push(() => ({ type: "attachments.saved", taskId: r.pick(p.threads), ...(r.chance(0.3) ? { steer: true } : {}), ...(r.chance(0.5) ? { text: "see this" } : {}), ids: ["img-1"], attachments: [{ path: "/tmp/a.png", labels: [] }] }), () => ({ type: "attachments.failed", taskId: r.pick(p.threads), message: "Too large" }));
  if (p.threads.length) options.push(() => ({ type: "computers.answered", taskId: r.pick(p.threads) }));
  options.push(() => ({ type: "dictation.model", model: r.pick(["missing", "ready", "unsupported"]) }));
  options.push(() => ({ type: "simulator.idle", seq: r.int(5) }));
  return options;
}

// What only the Go app has, which the TypeScript app never writes: idle compaction's setting, its
// session record, its timer, and any patch to them.
const GO_ONLY_KEYS = new Set(["idleCompaction", "idleCompactions"]);
function withoutGoOnly(value) {
  if (Array.isArray(value)) {
    return value
      .filter((item) => item?.type !== "schedule-idle-compaction" && !(Array.isArray(item?.path) && item.path.some((key) => GO_ONLY_KEYS.has(key))))
      .map(withoutGoOnly);
  }
  if (value === null || typeof value !== "object") return value;
  return Object.fromEntries(Object.entries(value).filter(([key]) => !GO_ONLY_KEYS.has(key)).map(([key, item]) => [key, withoutGoOnly(item)]));
}

// Strict JSON equality up to key order; JSON already drops undefined.
function diff(a, b, at = "$", out = []) {
  if (out.length > (global.CAP ?? 40)) return out;
  if (a === b) return out;
  if (a === null || b === null || typeof a !== "object" || typeof b !== "object") {
    out.push(`${at}: ts=${JSON.stringify(a)?.slice(0, 200)} go=${JSON.stringify(b)?.slice(0, 200)}`);
    return out;
  }
  if (Array.isArray(a) !== Array.isArray(b)) { out.push(`${at}: array mismatch ts=${JSON.stringify(a).slice(0, 200)} go=${JSON.stringify(b).slice(0, 200)}`); return out; }
  if (Array.isArray(a)) {
    if (a.length !== b.length) out.push(`${at}: length ts=${a.length} go=${b.length} ts=${JSON.stringify(a).slice(0, 300)} go=${JSON.stringify(b).slice(0, 300)}`);
    for (let i = 0; i < Math.min(a.length, b.length); i++) diff(a[i], b[i], `${at}[${i}]`, out);
    return out;
  }
  for (const key of new Set([...Object.keys(a), ...Object.keys(b)])) {
    const left = a[key], right = b[key];
    if ((left === undefined) !== (right === undefined)) { out.push(`${at}.${key}: ts=${JSON.stringify(left)?.slice(0, 200)} go=${JSON.stringify(right)?.slice(0, 200)}`); continue; }
    diff(left, right, `${at}.${key}`, out);
  }
  return out;
}

// Scripted stretches that walk a feature end to end; each step reads the state it lands in and may pass (null).
const pick = (r, list) => list.length ? r.pick(list) : undefined;
const dockOf = (state) => (state.docks ?? {})[state.currentId] ?? Object.values(state.docks ?? {})[0];
const tabOf = (state) => dockOf(state)?.browserTabs?.at(-1)?.id;
const terminalOf = (state) => dockOf(state)?.terminals?.at(-1)?.id;
const localThreads = (state) => (state.threads ?? []).filter((t) => !t.archivedAt).map((t) => t.id);
const remoteThreads = (state) => (state.computers?.paired ?? []).flatMap((c) => (c.state?.threads ?? []).map((t) => t.id));
const runOf = (state) => Object.values(state.activeRuns ?? {})[0];
const runEvent = (run, body) => run && { type: "run.event", event: { taskId: run.taskId, runId: run.runId, sequence: (run.sequence ?? 0) + 1, ...body } };
const SCENARIOS = {
  browser: (r) => [
    (s) => ({ type: "task.select", taskId: pick(r, localThreads(s)) ?? "t-1" }),
    () => ({ type: "browser.open", url: "https://example.com" }),
    (s) => tabOf(s) && { type: "browser.updated", page: { tabId: tabOf(s), url: "https://example.com/", title: "Example", loading: false, canGoBack: false, canGoForward: false } },
    () => ({ type: "browser.open", url: r.pick(["https://example.org", "example.net"]), ...(r.chance(0.3) ? { newTab: true } : {}) }),
    (s) => ({ type: "browser.go", delta: r.pick([-1, 1]), ...(r.chance(0.5) && tabOf(s) ? { tabId: tabOf(s) } : {}) }),
    () => ({ type: "browser.reload" }),
    (s) => tabOf(s) && { type: "browser.act", tabId: tabOf(s), action: { kind: "click", ref: "e1" } },
    (s) => tabOf(s) && { type: "view.find-open", target: { kind: "browser", tabId: tabOf(s) } },
    () => ({ type: "view.find-query", query: r.pick(["exa", ""]) }),
    (s) => tabOf(s) && { type: "find.results", target: { kind: "browser", tabId: tabOf(s) }, results: { matches: 3, index: 0 } },
    () => ({ type: "view.find-step", delta: r.pick([-1, 1]) }),
    () => r.pick([{ type: "view.escape" }, { type: "view.find-close" }]),
    (s) => tabOf(s) && { type: "browser.select-tab", tabId: tabOf(s) },
    () => ({ type: "browser.new-tab" }),
    (s) => tabOf(s) && { type: "browser.close-tab", tabId: tabOf(s) },
  ],
  agentBrowser: (r) => [
    (s) => { const run = runOf(s); return run && { type: "browser.open", taskId: run.taskId, url: "https://agent.example.com" }; },
    (s) => s.browserApproval && { type: "browser.decide", approvalId: s.browserApproval.approvalId, allow: r.chance(0.7) },
    (s) => { const run = runOf(s); return run && { type: "browser.open", taskId: run.taskId, url: "https://agent.example.com/two" }; },
  ],
  terminal: (r) => [
    (s) => ({ type: "task.select", taskId: pick(r, localThreads(s)) ?? "t-1" }),
    () => ({ type: "terminal.open", ...(r.chance(0.5) ? { cwd: "/repo" } : {}) }),
    (s) => terminalOf(s) && { type: "terminal.updated", update: { terminalId: terminalOf(s), title: "zsh", status: "running" } },
    (s) => terminalOf(s) && { type: "terminal.input", terminalId: terminalOf(s), data: "ls\r" },
    (s) => terminalOf(s) && { type: "view.find-open", target: { kind: "terminal", terminalId: terminalOf(s) } },
    () => ({ type: "view.find-query", query: "ls" }),
    (s) => terminalOf(s) && { type: "find.results", target: { kind: "terminal", terminalId: terminalOf(s) }, results: { matches: 1 } },
    () => ({ type: "view.find-close" }),
    (s) => terminalOf(s) && { type: "terminal.resize", terminalId: terminalOf(s), cols: 100, rows: 30 },
    (s) => terminalOf(s) && { type: r.pick(["terminal.restart", "terminal.close"]), terminalId: terminalOf(s) },
  ],
  snooze: (r) => [
    (s) => ({ type: "task.snooze", taskId: pick(r, localThreads(s)) ?? "t-2", hours: r.pick([1, 3, 24]) }),
    (s) => ({ type: "snoozes.elapsed", at: (s.threads ?? []).find((t) => t.snoozedUntil)?.snoozedUntil ?? BASE }),
    (s) => { const t = (s.threads ?? []).find((x) => x.snoozedUntil); return t && { type: "task.unsnooze", taskId: t.id }; },
  ],
  worktree: (r) => [
    () => ({ type: "task.select", taskId: r.pick(["t-1", "t-5", "t-2"]) }),
    () => ({ type: "task.checkout-branch", branch: r.pick(["feature/x", "main"]), ...(r.chance(0.5) ? { create: true } : {}) }),
    () => ({ type: "checkout.finished", ...(r.chance(0.3) ? { message: "conflict" } : {}) }),
    () => ({ type: "worktree.reveal", root: r.pick(["/wt/wt-1", "/wt/wt-2", "/nope"]) }),
    () => ({ type: "worktree.refresh" }),
    (s) => ({ type: "worktrees.loaded", worktrees: (s.worktrees ?? []).map((w) => ({ id: w.id, root: w.root, repository: "/repo", branch: r.pick(["main", null]), status: { changedFiles: r.int(3), comparison: r.chance(0.5) ? { branch: "main", ahead: r.int(3) } : null } })) }),
    () => ({ type: "task.move-worktree", taskId: r.pick(["t-1", "t-2", "t-5"]), destination: r.pick([{ kind: "local" }, { kind: "new" }, { kind: "worktree", id: "wt-1" }, { kind: "worktree", id: "wt-2" }]) }),
    () => ({ type: "worktree.confirm-delete", root: r.pick(["/wt/wt-2", "/wt/wt-1", null]) }),
    () => ({ type: "worktree.delete", ...r.pick([{ root: "/wt/wt-2" }, { worktreeId: "wt-1" }, { taskId: "t-5" }]), ...(r.chance(0.3) ? { missingOnly: true } : {}) }),
    () => ({ type: "task.archive", taskId: r.pick(["t-5", "t-1"]) }),
    () => r.pick([{ type: "worktree.released", taskId: "t-5", snapshot: { commit: "abcdef1234", shortCommit: "abcdef1", ref: "refs/aicodingtool/wt-1" } }, { type: "worktree.release-failed", taskId: "t-5", message: "busy" }]),
    () => ({ type: "task.restore", taskId: "t-5" }),
    () => ({ type: "task.set-worktree", taskId: r.pick(["t-2", "t-4"]), worktree: r.chance(0.7) }),
  ],
  simulator: (r) => [
    () => ({ type: "simulator.open" }),
    () => ({ type: "simulator.listed", availability: { status: "available" }, devices: [{ udid: "SIM-1", name: "iPhone 17", runtime: "iOS 27", state: r.pick(["booted", "shutdown"]) }, { udid: "SIM-2", name: "iPad", runtime: "iOS 27", state: "shutdown" }] }),
    () => ({ type: "simulator.select", udid: r.pick(["SIM-1", "SIM-2"]) }),
    () => ({ type: r.pick(["simulator.booted", "simulator.failed"]), udid: "SIM-2", message: "Boot failed" }),
    () => ({ type: "simulator.input", input: r.pick([{ kind: "tap", x: 0.5, y: 0.5 }, { kind: "rotate", orientation: "landscape-left" }, { kind: "button", button: "home" }]) }),
    (s) => { const run = runOf(s); return run && { type: "simulator.input", taskId: run.taskId, input: { kind: "tap", x: 0.2, y: 0.3 } }; },
    () => ({ type: "simulator.turned", udid: "SIM-1", orientation: "landscape-left" }),
    () => ({ type: "simulator.idle", seq: r.int(3) }),
    () => ({ type: "simulator.refresh" }),
  ],
  dictation: (r) => [
    () => ({ type: "dictation.model", model: r.pick(["ready", "ready", "missing"]) }),
    () => ({ type: "dictation.start" }),
    (s) => s.dictation?.session && { type: "dictation.started", id: s.dictation.session.id },
    () => ({ type: r.pick(["dictation.hands-free", "dictation.stop", "dictation.cancel"]), ...(r.chance(0.3) ? { send: true } : {}) }),
    (s) => s.dictation?.session && r.pick([{ type: "dictation.transcribed", id: s.dictation.session.id, text: r.pick(["fix the bug", ""]) }, { type: "dictation.failed", id: s.dictation.session.id, message: "No mic" }]),
    () => ({ type: "dictation.model", model: "missing" }),
    () => ({ type: "dictation.download" }),
    () => r.pick([{ type: "dictation.cancel-download" }, { type: "dictation.model", model: "ready", progress: 1 }]),
  ],
  appUpdate: (r) => [
    () => ({ type: "app.update-changed", update: { status: "available", version: "0.11.0" } }),
    () => r.pick([{ type: "app.download-update" }, { type: "app.update" }]),
    () => ({ type: "app.update-changed", update: r.pick([{ status: "downloading", version: "0.11.0", percent: 50 }, { status: "ready", version: "0.11.0" }]) }),
    () => ({ type: "app.update-changed", update: { status: "ready", version: "0.11.0" } }),
    () => r.pick([{ type: "app.install-update" }, { type: "app.cancel-update" }, { type: "app.update" }]),
  ],
  computers: (r) => [
    () => ({ type: "computers.changed", name: "This Mac", links: [{ id: "c-1", name: "Mac mini", host: "mini.local", status: "connected", error: null, pairedAt: BASE }] }),
    (s) => ({ type: "computer.state", id: "c-1", state: remoteState(s) }),
    () => ({ type: "computers.filter", filter: r.pick(["c-1", "all", "this"]) }),
    (s) => { const id = pick(r, remoteThreads(s)); return id && { type: "task.select", taskId: id }; },
    (s) => { const id = pick(r, remoteThreads(s)); return id && { type: "task.send", taskId: id, text: "Do it remotely" }; },
    (s, o) => { const sent = o.find((effect) => effect.type === "computer.forward" && effect.draft); return sent && { type: "computers.forwarded", draft: sent.draft }; },
    (s) => { const id = pick(r, remoteThreads(s)); return id && r.pick([{ type: "task.rename", taskId: id, title: "Remote renamed" }, { type: "task.archive", taskId: id }, { type: "run.cancel", taskId: id }, { type: "task.set-model", taskId: id, engine: "claude", model: "claude-opus-4-7" }]); },
    () => ({ type: "computers.label", id: "c-1", name: r.pick(["Lab", ""]) }),
    () => ({ type: "computers.rename", name: r.pick(["Laptop", ""]) }),
    () => ({ type: "project.add", computerId: "c-1", root: "/remote/repo" }),
    () => ({ type: "computers.changed", name: "This Mac", links: [{ id: "c-1", name: "Mac mini", host: "mini.local", status: r.pick(["offline", "connecting", "connected"]), error: r.pick([null, "lost"]), pairedAt: BASE }] }),
    () => r.chance(0.3) ? { type: "computers.forget", id: "c-1" } : null,
  ],
  browserImport: (r) => [
    () => ({ type: "browser-import.read" }),
    () => ({ type: "browser-import.sources", sources: [{ id: "chrome:Default", browser: "Chrome", profile: "Default" }, { id: "safari", browser: "Safari", profile: "" }] }),
    () => ({ type: "browser-import.choose", sourceId: r.pick(["chrome:Default", "safari", null]) }),
    (s) => ({ type: "browser-import.sites", sourceId: s.browserImport?.sourceId ?? "chrome:Default", sites: [{ domain: "github.com", cookies: 12 }, { domain: "example.com", cookies: 2 }] }),
    () => ({ type: "browser-import.toggle", site: r.pick(["github.com", "example.com"]) }),
    () => ({ type: "browser-import.filter", text: r.pick(["git", ""]) }),
    () => ({ type: "browser-import.run" }),
    (s) => r.pick([{ type: "browser-import.done", sourceId: s.browserImport?.sourceId ?? "chrome:Default", sites: ["github.com"], result: { imported: 3, skipped: 1 } }, { type: "browser-import.failed", message: "Keychain refused" }]),
    () => ({ type: "browser-import.again" }),
  ],
  image: (r) => [
    () => ({ type: "image.open", source: "attachment://file/tmp/a.png", ...(r.chance(0.6) ? { gallery: ["attachment://file/tmp/a.png", "message-image://file/tmp/b.png"] } : {}) }),
    () => ({ type: "image.step", delta: r.pick([-1, 1]) }),
    () => ({ type: "image.download" }),
    () => ({ type: "image.close" }),
  ],
  run: (r) => [
    (s) => ({ type: "task.send", taskId: pick(r, localThreads(s)) ?? "t-1", text: "Run the tests" }),
    (s) => { const [id, pending] = Object.entries(s.pendingRuns ?? {})[0] ?? []; return id && { type: "run.resolved", pendingId: id, workspace: { id: "workspace-1", kind: "project", root: "/repo" } }; },
    (s) => runEvent(runOf(s), { type: "run.status", status: "running" }),
    (s) => runEvent(runOf(s), { type: "assistant.delta", messageId: "reply", text: r.pick(["Here: ![shot](/tmp/shot.png)", "Done.\n\n"]), append: true }),
    (s) => runEvent(runOf(s), { type: "approval.requested", approvalId: "approval-s", title: "Run a command", description: "rm x", intent: { toolId: "tool-s", name: "Bash", input: { command: "rm x" } } }),
    (s) => { const run = runOf(s); return run && { type: "run.decide", taskId: run.taskId, runId: run.runId, approvalId: "approval-s", allow: r.chance(0.6) }; },
    (s) => runEvent(runOf(s), { type: "question.requested", requestId: "q-s", request: { blocking: true, questions: [{ id: "q", header: "Pick", question: "Which?", options: [{ label: "A", description: "" }, { label: "B", description: "" }] }] } }),
    (s) => { const run = runOf(s); return run && { type: "question.set-answer", taskId: run.taskId, runId: run.runId, requestId: "q-s", questionId: "q", text: "A" }; },
    (s) => { const run = runOf(s); return run && { type: "question.answer", taskId: run.taskId, runId: run.runId, requestId: "q-s", questionId: "q", text: "A" }; },
    (s) => { const run = runOf(s); return run && { type: "task.send", taskId: run.taskId, text: "and also this", ...(r.chance(0.5) ? { steer: true } : {}) }; },
    (s) => runEvent(runOf(s), { type: "run.status", status: r.pick(["succeeded", "failed", "cancelled"]) }),
  ],
  projects: (r) => [
    () => ({ type: "project.open", ...(r.chance(0.5) ? { start: true } : {}) }),
    () => ({ type: "view.add-project-path", root: r.pick(["/re", "/repo/", "~/code"]) }),
    (s) => s.projectAdd && { type: "project.directories", request: s.projectAdd.request, directories: ["/repo/a", "/repo/b"], ...(r.chance(0.2) ? { error: "No such folder" } : {}) },
    () => ({ type: "view.add-project-key", key: r.pick(["ArrowUp", "ArrowDown", "Tab", "Enter"]) }),
    () => ({ type: "view.add-project-accept", index: r.int(2) }),
    () => ({ type: "view.add-project-pick" }),
    (s) => s.projectAdd && { type: "project.path-picked", request: s.projectAdd.request, ...(r.chance(0.8) ? { root: "/picked" } : { error: "Cancelled" }) },
    () => ({ type: "view.add-project-submit" }),
    (s) => r.pick([
      { type: "project.added", workspace: { id: "ws-new", kind: "project", root: s.projectAdd?.root || "/new/repo" }, ...(s.projectAdd ? { request: s.projectAdd.request } : {}), ...(s.projectAdd?.start ? { start: true } : {}) },
      { type: "project.add-finished", ...(s.projectAdd ? { request: s.projectAdd.request } : {}), error: "Not a folder" },
    ]),
    () => ({ type: "project.add", root: "/another/repo", ...(r.chance(0.5) ? { start: true } : {}) }),
    () => r.pick([{ type: "project.added", workspace: { id: "ws-another", kind: "project", root: "/another/repo" } }, { type: "project.add-finished", error: "Not a folder" }]),
    () => ({ type: "project.edit", projectId: "project-2", ...r.pick([{ root: "/moved/long-project" }, { name: "Renamed" }, { name: null }]) }),
    () => r.pick([{ type: "project.registered", projectId: "project-2", workspace: { id: "ws-moved", kind: "project", root: "/moved/long-project" } }, { type: "project.register-failed", projectId: "project-2", message: "No such folder" }]),
  ],
  review: (r) => [
    () => ({ type: "task.select", taskId: r.pick(["t-5", "t-5", "t-1"]) }),
    () => ({ type: "review.open" }),
    () => ({ type: "review.set-step", step: r.pick(["targets", "base", "commit", "custom"]) }),
    () => r.chance(0.2) ? { type: "review.close" } : { type: "review.start", target: r.pick([{ type: "uncommittedChanges" }, { type: "baseBranch", branch: "main" }, { type: "commit", sha: "abc1234", title: r.pick([null, "Fix"]) }, { type: "custom", instructions: "Check the tests" }]) },
    (s) => { const [id] = Object.keys(s.pendingRuns ?? {}); return id && { type: "run.resolved", pendingId: id, workspace: { id: "ws-wt-1", kind: "worktree", root: "/wt/wt-1" } }; },
    (s) => runEvent(runOf(s), { type: "run.status", status: r.pick(["running", "succeeded"]) }),
    () => ({ type: "run.compact", taskId: r.pick(["t-1", "t-5"]) }),
  ],
  coordination: (r) => [
    () => ({ type: "task.set-role", taskId: "t-1", role: "coordinator" }),
    () => ({ type: "task.send", coordinatorId: "t-1", text: "Write the tests", brief: { intent: "tests", doneWhen: "they pass", delivers: r.pick(["commit", "report", "pull-request"]) }, ...(r.chance(0.5) ? { worktree: true } : {}) }),
    (s) => { const member = (s.threads ?? []).find((t) => t.coordinatorId === "t-1"); return member && { type: "coordination.reported", taskId: member.id, state: r.pick(["working", "done", "blocked"]), summary: "Progress" }; },
    () => ({ type: "coordination.decision-raised", taskId: "t-1", request: { question: "Which way?", options: [{ label: "Left" }, { label: "Right", recommended: true }] }, decisionId: "d-s" }),
    () => ({ type: "decision.answer", taskId: "t-1", decisionId: "d-s", answer: r.pick(["Left", "Right"]) }),
    () => ({ type: "coordination.proposed", taskId: "t-1", proposals: [{ id: "p-s", title: "Write docs", prompt: "Write the docs", proposedAt: BASE, place: r.pick([{}, { worktree: true }, { project: "project-2" }]) }] }),
    () => ({ type: r.pick(["proposal.start", "proposal.dismiss"]), taskId: "t-1", proposalId: "p-s" }),
    () => ({ type: "coordination.remembered", taskId: "t-1", change: r.pick([{ kind: "purpose", purpose: "Ship it" }, { kind: "remember", text: "Use pnpm" }]) }),
    (s) => { const member = (s.threads ?? []).find((t) => t.coordinatorId === "t-1"); return member && { type: "coordination.next-step", taskId: member.id, step: r.pick(["fix-ci", "merge", "create-pull-request"]) }; },
    () => ({ type: "coordination.set-muted", taskId: "t-1", muted: r.chance(0.5) }),
    () => ({ type: "coordination.stop-all", taskId: "t-1" }),
  ],
  sideChat: (r) => [
    (s) => ({ type: "task.select", taskId: pick(r, localThreads(s)) ?? "t-1" }),
    () => ({ type: "side-chat.open", chatId: `side-${r.int(3)}` }),
    (s) => { const chat = (s.sideChats ?? [])[0]; return chat && { type: "task.send", taskId: chat.id, text: "What does this do?" }; },
    () => ({ type: "view.select-dock-tab", tab: r.pick(["side-0", "side-1", "browser", "home"]) }),
    (s) => { const chat = (s.sideChats ?? [])[0]; return chat && { type: "side-chat.close", chatId: chat.id }; },
  ],
  dock: (r) => [
    (s) => ({ type: "task.select", taskId: pick(r, localThreads(s)) ?? "t-1" }),
    () => ({ type: "view.open-dock-panel", panel: r.pick(["changes", "terminal", "browser", "overview", "simulator", "workflow"]) }),
    () => ({ type: "view.set-dock-expanded", expanded: r.chance(0.5) }),
    () => ({ type: "view.select-dock-index", index: r.int(4) }),
    () => ({ type: "view.dock-keys", tab: r.pick(["browser", "terminal", "home"]) }),
    () => ({ type: "view.close-dock-panel", panel: r.pick(["changes", "terminal", "browser"]) }),
    () => ({ type: "view.shortcut", action: r.pick(SHORTCUT_ACTIONS), surface: r.pick(["any", "browser", "desktop"]) }),
  ],
};
const SCENARIO_NAMES = Object.keys(SCENARIOS);

const { applyWorkspacePatches } = require(path.join(dist, "main/application/workspace-patches.js"));
const has = (value, at) => at.every((key) => { const held = value !== null && typeof value === "object" && Object.hasOwn(value, key); value = held ? value[key] : undefined; return held; });

function sortedPatches(patches) {
  const key = (patch) => JSON.stringify(patch.path);
  return patches.map((patch, index) => [key(patch), index, patch]).sort((a, b) => a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : a[1] - b[1]).map(([, , patch]) => patch);
}

// Answers one outstanding effect, which then leaves the list.
function answerOutstanding(state, r, outstanding) {
  const order = outstanding.map((effect, index) => [effect, index]).sort(() => r.next() - 0.5);
  for (const [effect, index] of order) {
    const options = [...effectAnswers(state, r, effect), ...serviceAnswers(state, r, effect), ...moreAnswers(state, r, effect)];
    if (!options.length) continue;
    outstanding.splice(index, 1);
    return r.pick(options)();
  }
  return null;
}

async function runSeed(seed) {
  const ts = proc("node", [path.join(__dirname, "ts-reduce.cjs"), dist]);
  const rs = proc(goBin, []);
  const r = rng(seed);
  let now = BASE;
  const trail = [];
  const send = async (input) => {
    now += 1 + r.int(5000);
    const asked = { now, input };
    trail.push(asked);
    const [left, right] = await Promise.all([ts.ask(asked), rs.ask(asked)]);
    return [JSON.parse(left), withoutGoOnly(JSON.parse(right))];
  };
  const resetLeft = JSON.parse(await ts.ask({ reset: true }));
  const resetRight = withoutGoOnly(JSON.parse(await rs.ask({ reset: true })));
  let problems = diff(resetLeft.state, resetRight.state, "$state");
  if (problems.length) { report(seed, 0, { reset: true }, problems, trail); ts.close(); rs.close(); return false; }
  let state = resetLeft.state;
  let viewedState = resetLeft.state;
  let effects = [];
  let outstanding = [];
  let script = [];
  const first = [{ type: "store.loaded", data: seedStore(), hiddenTasks: 0 }];
  for (let step = 0; step < STEPS; step++) {
    const roll = r.next();
    if (!script.length && r.chance(0.04)) { const name = r.pick(SCENARIO_NAMES); script = SCENARIOS[name](r); COVER.scenarios[name] = (COVER.scenarios[name] ?? 0) + 1; }
    let scripted = null;
    while (step >= first.length && script.length && r.chance(0.7) && !scripted) scripted = script.shift()(state, outstanding) || null;
    let input = scripted ? scripted : step < first.length ? first[step]
      : roll < 0.12 ? sendCommand(state, r)
      : roll < 0.52 ? randomCommand(state, r)
      : roll < 0.72 ? answerOutstanding(state, r, outstanding)
      : randomEvent(state, r, outstanding);
    if (!input) input = randomEvent(state, r, outstanding);
    const [left, right] = await send(input);
    if (left.error && right.error) continue;
    if (left.error || right.error) {
      // Go refusing an input TS takes means a shape the port cannot read.
      problems = [`error: ts=${left.error ?? "ok"} go=${right.error ?? "ok"}`];
    } else {
      problems = [...diff(left.state, right.state, "$state"), ...diff(left.effects, right.effects, "$effects"), ...diff(left.result ?? null, right.result ?? null, "$result"), ...diff(left.ids, right.ids, "$ids")];
    }
    if (!problems.length && VIEWS_EVERY && step % VIEWS_EVERY === 0) {
      const threads = (left.state.threads ?? []).map((thread) => thread.id);
      const taskId = r.chance(0.5) && left.state.currentId ? left.state.currentId : r.pick([...threads, "nope"]);
      const thread = (left.state.threads ?? []).find((item) => item.id === taskId);
      const asked = { views: { at: now, taskId, before: r.int((thread?.messages?.length ?? 0) + 2), reference: r.pick([taskId, "fix", "Codex", "#2", "t-", "zzz", ""]) } };
      trail.push(asked);
      const [tv, gv] = (await Promise.all([ts.ask(asked), rs.ask(asked)])).map((line, index) => index ? withoutGoOnly(JSON.parse(line)) : JSON.parse(line));
      if (tv.views?.patches && gv.views?.patches) {
        // A key TS holds as undefined patches as `{ path }`, which sets it to undefined, and its removal
        // removes a key JSON never showed. Go has no such key: it removes one that was there and says
        // nothing of one that was not.
        tv.views.patches = tv.views.patches.flatMap((patch) => "value" in patch || patch.splice ? [patch] : has(viewedState, patch.path) ? [{ path: patch.path, remove: true }] : []);
        // Patches to different keys apply in any order; Go walks a record's fields in declaration order, TS in insertion order.
        tv.views.patches = sortedPatches(tv.views.patches);
        const applied = JSON.parse(JSON.stringify(applyWorkspacePatches(viewedState, gv.views.patches)));
        gv.views.patches = sortedPatches(gv.views.patches);
        problems = diff(left.state, applied, "$appliedGoPatches");
      }
      viewedState = left.state;
      problems = [...problems, ...(tv.error || gv.error ? [`views error: ts=${tv.error ?? "ok"} go=${gv.error ?? "ok"}`] : diff(tv.views, gv.views, "$views"))];
      COVER.views++;
      if (tv.views?.mobilePatch) COVER.mobilePatches++;
      if (tv.views?.patches?.length) COVER.windowPatches++;
    }
    if (problems.length) { report(seed, step, input, problems, trail); ts.close(); rs.close(); return false; }
    state = left.state;
    effects = left.effects ?? [];
    outstanding = [...effects.filter((effect) => effect.type !== "persist-preferences" && effect.type !== "browser.permissions" && effect.type !== "schedule-limit-reset" && effect.type !== "announce-thread"), ...outstanding].slice(0, 16);
    for (const effect of effects) COVER.effects[effect.type] = (COVER.effects[effect.type] ?? 0) + 1;
    if (Object.keys(state.activeRuns ?? {}).length) COVER.running++;
    if (Object.keys(state.pendingRuns ?? {}).length) COVER.pending++;
    if (input.type === "run.event") COVER.runEvents++;
    if ((state.threads ?? []).some((thread) => thread.resourceStop)) COVER.resourceStopped++;
    const kind = input.type === "thread.event" || input.type === "run.event" ? `${input.type}:${input.event.type}` : input.type;
    COVER.inputs[kind] = (COVER.inputs[kind] ?? 0) + 1;
    if (left.result && left.result.ok === false) COVER.refused++; else COVER.accepted++;
    if (VERBOSE) console.log(seed, step, input.type);
  }
  ts.close(); rs.close();
  return true;
}

function printCover() {
  const universe = require("./universe.cjs");
  const types = "internal/application/workspace_reducer_types_types.go", ipc = "internal/contracts/ipc_types.go";
  const inputs = universe(types, "WorkspaceInput").filter((type) => type !== "run.event" && type !== "thread.event");
  const runEvents = universe(ipc, "RunEvent").map((type) => `run.event:${type}`);
  const threadEvents = universe(ipc, "ThreadEvent").map((type) => `thread.event:${type}`);
  const effects = universe(types, "WorkspaceEffect");
  const all = [...inputs, ...runEvents, ...threadEvents];
  const missing = all.filter((type) => !COVER.inputs[type]);
  const missingEffects = effects.filter((type) => !COVER.effects[type]);
  const { inputs: seen, effects: made, unsampled, ...counts } = COVER;
  if (Object.keys(unsampled).length) console.log(`fields with no valid candidate: ${Object.keys(unsampled).join(" ")}`);
  console.log(JSON.stringify(counts));
  console.log(`inputs: ${all.length - missing.length}/${all.length} exercised; never: ${missing.join(" ") || "-"}`);
  console.log(`effects: ${effects.length - missingEffects.length}/${effects.length} produced; never: ${missingEffects.join(" ") || "-"}`);
  console.log(`effect counts: ${Object.entries(made).sort((a, b) => a[1] - b[1]).map(([type, n]) => `${type}=${n}`).join(" ")}`);
  const thin = Object.entries(seen).filter(([, n]) => n < 20).map(([type, n]) => `${type}=${n}`);
  if (thin.length) console.log(`thin (<20): ${thin.join(" ")}`);
}

function report(seed, step, input, problems, trail) {
  console.log(`\n# seed ${seed} step ${step}: ${JSON.stringify(input).slice(0, 400)}`);
  for (const problem of problems) console.log("  " + problem);
  fs.writeFileSync(path.join(process.env.TRAILS ?? "/tmp", `trail-${seed}.jsonl`), trail.map((line) => JSON.stringify(line)).join("\n") + "\n");
}

(async () => {
  let passed = 0;
  for (let seed = seedFrom; seed <= seedTo; seed++) if (await runSeed(seed)) passed++;
  if (rest.includes("--cover")) printCover();
  console.log(`\n${passed}/${seedTo - seedFrom + 1} seeds agree over ${STEPS} steps; ${commands.length} command types.`);
})();
