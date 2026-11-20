// Runs application.Reduce (cmd/reduce-lines) beside the TypeScript reducer inside the TypeScript test
// suite (see spec.sh). Each reduce the tests make is answered by TypeScript; the same state and input go
// to Go with the clock pinned to one reading, and every difference is logged to ACT_GO_LOG.
import { execFileSync, spawn } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterAll, expect } from "vitest";

type Pipes = { write: number; read: number; pending: string };
let pipes: Pipes | null = null;

function open(): Pipes {
  if (pipes) return pipes;
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "act-go-"));
  const requests = path.join(dir, "req");
  const answers = path.join(dir, "ans");
  execFileSync("mkfifo", [requests, answers]);
  const child = spawn(process.env.ACT_GO_BIN!, [requests, answers], { stdio: ["ignore", "ignore", "inherit"] });
  child.unref();
  const write = fs.openSync(requests, "w");
  const read = fs.openSync(answers, "r");
  // Both ends are open, so the pipes' names can go; a worker may never see its own exit.
  fs.rmSync(dir, { recursive: true, force: true });
  process.on("exit", () => { try { fs.closeSync(write); child.kill(); } catch {} });
  pipes = { write, read, pending: "" };
  return pipes;
}

const plain = (_key: string, value: unknown) => value instanceof Set ? [...value] : value instanceof Map ? Object.fromEntries(value) : value;

function ask(value: unknown): any {
  const p = open();
  fs.writeSync(p.write, JSON.stringify(value, plain) + "\n");
  const buffer = Buffer.alloc(1 << 20);
  for (;;) {
    const newline = p.pending.indexOf("\n");
    if (newline >= 0) {
      const line = p.pending.slice(0, newline);
      p.pending = p.pending.slice(newline + 1);
      return JSON.parse(line);
    }
    const n = fs.readSync(p.read, buffer, 0, buffer.length, null);
    if (n === 0) throw new Error("reduce-lines closed");
    p.pending += buffer.toString("utf8", 0, n);
  }
}

const PLACEHOLDER = /00000000-0000-4000-8000-(\d{12})/g;
function replaceIds(value: any, ids: string[]): any {
  if (typeof value === "string") return value.replace(PLACEHOLDER, (match, n) => ids[Number(n)] ?? match);
  if (Array.isArray(value)) return value.map((item) => replaceIds(item, ids));
  if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).map(([key, item]) => [replaceIds(key, ids), replaceIds(item, ids)]));
  return value;
}

function diff(a: any, b: any, at = "$", out: string[] = []): string[] {
  if (out.length > 12) return out;
  if (a === b) return out;
  if (a === null || b === null || typeof a !== "object" || typeof b !== "object") {
    out.push(`${at}: ts=${JSON.stringify(a)?.slice(0, 300)} go=${JSON.stringify(b)?.slice(0, 300)}`);
    return out;
  }
  if (Array.isArray(a) !== Array.isArray(b)) { out.push(`${at}: array mismatch ts=${JSON.stringify(a).slice(0, 300)} go=${JSON.stringify(b).slice(0, 300)}`); return out; }
  if (Array.isArray(a)) {
    if (a.length !== b.length) out.push(`${at}: length ts=${a.length} go=${b.length} ts=${JSON.stringify(a).slice(0, 300)} go=${JSON.stringify(b).slice(0, 300)}`);
    for (let i = 0; i < Math.min(a.length, b.length); i++) diff(a[i], b[i], `${at}[${i}]`, out);
    return out;
  }
  for (const key of new Set([...Object.keys(a), ...Object.keys(b)])) {
    if ((a[key] === undefined) !== (b[key] === undefined)) { out.push(`${at}.${key}: ts=${JSON.stringify(a[key])?.slice(0, 300)} go=${JSON.stringify(b[key])?.slice(0, 300)}`); continue; }
    diff(a[key], b[key], `${at}.${key}`, out);
  }
  return out;
}

const json = (value: unknown) => JSON.parse(JSON.stringify(value, plain) ?? "null");
let depth = 0;
let calls = 0;
afterAll(() => { if (calls) fs.appendFileSync(process.env.ACT_GO_LOG!, JSON.stringify({ calls }) + "\n"); calls = 0; });

function log(entry: Record<string, unknown>) {
  let test = "";
  try { test = expect.getState().currentTestName ?? ""; } catch {}
  fs.appendFileSync(process.env.ACT_GO_LOG!, JSON.stringify({ test, file: expect.getState().testPath?.split("/tests/")[1], ...entry }, plain) + "\n");
}

/** Answers with TypeScript's transition, after checking Go's against it. Nested reduces are TypeScript's alone. */
export function differential(tsReduce: (state: any, input: any) => any, state: any, input: any): any {
  if (depth > 0) return tsReduce(state, input);
  const clock = Date.now;
  const at = clock.call(Date);
  const descriptor = Object.getOwnPropertyDescriptor(globalThis.crypto, "randomUUID");
  const original = globalThis.crypto.randomUUID.bind(globalThis.crypto);
  const ids: string[] = [];
  let ts: any;
  depth++;
  try {
    Object.defineProperty(globalThis.crypto, "randomUUID", { value: () => { const id = original(); ids.push(id); return id; }, configurable: true, writable: true });
    Date.now = () => at;
    ts = tsReduce(state, input);
  } finally {
    Date.now = clock;
    if (descriptor) Object.defineProperty(globalThis.crypto, "randomUUID", descriptor);
    depth--;
  }
  calls++;
  const kind = input?.type === "run.event" || input?.type === "thread.event" ? `${input.type}:${input.event?.type}` : input?.type;
  let answer: any;
  try { answer = ask({ now: at, state, input }); } catch (error) { log({ kind, error: String(error) }); return ts; }
  if (answer.error) { log({ kind, error: answer.error.slice(0, 600) }); return ts; }
  if (Number(answer.ids ?? 0) !== ids.length) log({ kind, problems: [`ids: ts=${ids.length} go=${answer.ids}`] });
  const go = replaceIds(answer, ids);
  const problems = [...diff(json(ts.state), go.state, "$state"), ...diff(json(ts.effects), go.effects ?? [], "$effects"), ...diff(json(ts.result ?? null), go.result ?? null, "$result")];
  if (problems.length) log({ kind, problems, input: JSON.stringify(input, plain)?.slice(0, 400), ...(process.env.ACT_GO_DUMP ? { asked: { now: at, state, input } } : {}) });
  return ts;
}

const threadIds = (threads: any[]) => threads.map((thread) => thread.id);
const SHAPES: Record<string, (value: any) => unknown> = {
  threadLists: (value) => ({ listed: threadIds(value.listedThreads), visible: threadIds(value.visibleThreads), archived: threadIds(value.archivedThreads), worktreeThreadIds: [...value.worktreeThreadIds].sort(), sideChatAttention: [...value.sideChatAttention].sort(), unreadCount: value.unreadCount }),
  findThread: (value) => value?.id ?? null,
};

/** Answers with the TypeScript projection, after checking cmd/reduce-lines' `call` of the same name against it. */
export function differentialCall(name: string, tsCall: () => any, args: any[]): any {
  if (depth > 0) return tsCall();
  const clock = Date.now;
  const at = clock.call(Date);
  let ts: any;
  depth++;
  try {
    Date.now = () => at;
    ts = tsCall();
  } finally {
    Date.now = clock;
    depth--;
  }
  calls++;
  const [state, ...rest] = name === "remoteUnreadCount" ? [{ computers: args[0] }] : args;
  let answer: any;
  try { answer = ask({ call: name, now: at, state, args: rest }); } catch (error) { log({ kind: `call:${name}`, error: String(error) }); return ts; }
  if (answer.error) { log({ kind: `call:${name}`, error: answer.error.slice(0, 600) }); return ts; }
  const problems = diff(json((SHAPES[name] ?? ((value: any) => value))(ts)), answer.value ?? null, `$${name}`);
  if (problems.length) log({ kind: `call:${name}`, problems, ...(process.env.ACT_GO_DUMP ? { asked: { now: at, state, input: { args: rest } } } : {}) });
  return ts;
}

const checked = new Map<string, Set<string>>();
const SWAPS: unknown[] = [null, "", "x", 0, -1, 1.5, 2 ** 53, true, [], {}, ["x"], "a".repeat(5000)];

/** Every value one key away from `value`: each key left out, or swapped for something else, one level down too. */
function mutations(value: unknown): unknown[] {
  if (!value || typeof value !== "object") return [];
  const out: unknown[] = [];
  const entries = Array.isArray(value) ? value.map((item, index) => [index, item] as const) : Object.entries(value);
  for (const [key, item] of entries) {
    const without: any = Array.isArray(value) ? value.filter((_, index) => index !== key) : { ...value };
    if (!Array.isArray(value)) delete without[key];
    out.push(without);
    for (const swap of SWAPS) out.push(Array.isArray(value) ? value.map((other, index) => index === key ? swap : other) : { ...value, [key]: swap });
    for (const inner of mutations(item).slice(0, 40)) out.push(Array.isArray(value) ? value.map((other, index) => index === key ? inner : other) : { ...value, [key]: inner });
  }
  return out;
}

/** Answers with the TypeScript check, after asking cmd/reduce-lines' `check` of the same name about it and values near it. */
export function differentialCheck(name: string, tsCheck: (value: unknown) => boolean, args: unknown[]): boolean {
  const ts = tsCheck(...(args as [unknown]));
  if (depth > 0) return ts;
  const seen = checked.get(name) ?? new Set<string>();
  checked.set(name, seen);
  const first = JSON.stringify(args[0], plain);
  if (first === undefined || seen.has(first) || seen.size > 400) return ts;
  seen.add(first);
  depth++;
  try {
    for (const value of [args[0], ...mutations(JSON.parse(first)).slice(0, 300)]) {
      const text = JSON.stringify(value, plain);
      if (value !== args[0] && seen.has(text)) continue;
      seen.add(text);
      calls++;
      const expected = value === args[0] ? ts : tsCheck(value);
      const answer = ask({ check: name, value });
      if (answer.error) { log({ kind: `check:${name}`, error: answer.error.slice(0, 600) }); continue; }
      if (answer.value !== expected) log({ kind: `check:${name}`, problems: [`$${name}: ts=${expected} go=${answer.value}`], input: text.slice(0, 600) });
    }
  } finally {
    depth--;
  }
  return ts;
}
