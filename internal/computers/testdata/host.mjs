// A paired host on the Electron app's own bridge, driven by a Go test over stdin and stdout, one JSON
// line each way. Run as: node host.mjs <ai-coding-tool checkout>
import { mkdtempSync, rmSync } from "node:fs";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import readline from "node:readline";
import { pathToFileURL } from "node:url";

const checkout = process.argv[2];
const dist = path.join(checkout, "dist", "main");
const load = (file) => import(pathToFileURL(path.join(dist, file)).href);
const require = createRequire(path.join(dist, "main", "serve.js"));

const { MobileServer, WORKSPACE_SOCKET_PATH } = await load("main/mobile/mobile-server.mjs");
const { PairingStore } = await load("main/mobile/pairing.mjs");
const { emptyWorkspaceState } = require(path.join(dist, "application", "workspace-state.js"));
const { rankedThread } = require(path.join(dist, "domain", "thread-storage.js"));

function task(id, overrides = {}) {
  return rankedThread({ id, title: id, engine: "claude", executionPolicy: "confirm", messages: [], continuationStatus: "none", lastChangeSnapshot: { files: [], capturedAt: 1 }, updatedAt: 1, ...overrides });
}

const folder = mkdtempSync(path.join(os.tmpdir(), "aicodingtool-go-host-"));
const devices = new PairingStore(path.join(folder, "mobile-devices.v1.json"));
const authenticate = devices.authenticate.bind(devices);
let state = { ...emptyWorkspaceState(), threads: [task("first", { title: "First" })] };
let revision = 0;
let name = "linux-box";
let release = () => {};
let gate = Promise.resolve();
const listeners = new Set();
const inputs = [];
const queries = [];

const server = new MobileServer({
  devices,
  port: 0,
  allowedOrigins: () => [],
  snapshot: async () => { throw new Error("no phones here"); },
  command: async () => {},
  query: async () => null,
  onChange: () => {},
  sessionGraceMs: 60_000,
  workspace: {
    name: () => name,
    snapshot: () => ({ revision, state }),
    subscribe: (listener) => { listeners.add(listener); return () => { listeners.delete(listener); }; },
    input: async (batch) => {
      inputs.push(...batch);
      if (batch.some((input) => input.type === "task.rename" && input.title === "slow")) await gate;
      if (batch.some((input) => input.type === "task.rename" && input.title === "refuse")) return { ok: false, message: "Refused by the host", revision };
      return { ok: true, revision };
    },
    query: async (query) => {
      queries.push(query);
      if (query.kind === "directories") return ["/held/there/"];
      return { status: "available", patch: `patch for ${query.kind}` };
    },
  },
});
await server.start("127.0.0.1");

const ops = {
  url: () => `ws://127.0.0.1:${server.port}${WORKSPACE_SOCKET_PATH}`,
  port: () => server.port,
  mint: () => devices.mint(Date.now()).code,
  devices: () => devices.list().map((device) => ({ id: device.id, kind: device.kind, name: device.name })),
  inputs: () => inputs,
  queries: () => queries,
  gate: () => { gate = new Promise((resolve) => { release = resolve; }); },
  release: () => release(),
  publish: ({ next }) => {
    const changed = Object.fromEntries(Object.entries(next).map(([key, value]) => [key, key === "threads" ? value.map((thread) => task(thread.id, thread)) : value]));
    state = { ...state, ...changed };
    revision += 1;
    const update = { revision, patches: Object.entries(changed).map(([key, value]) => ({ path: [key], value })) };
    for (const listener of listeners) listener(update);
  },
  notice: ({ notice }) => server.notice(notice),
  rename: ({ name: next }) => { name = next; server.announceName(); },
  refuse: ({ on }) => { devices.authenticate = on ? () => null : authenticate; },
  unreadable: () => {
    devices.authenticate = () => null;
    Object.defineProperty(devices, "unreadable", { get: () => true });
  },
  restart: async () => { await server.stop(); await server.start("127.0.0.1"); return server.port; },
  drop: ({ deviceId }) => server.dropDevice(deviceId),
  quit: async () => {
    await server.stop();
    rmSync(folder, { recursive: true, force: true });
    process.exit(0);
  },
};

const lines = readline.createInterface({ input: process.stdin });
lines.on("line", async (text) => {
  const { id, op, ...args } = JSON.parse(text);
  try {
    const value = await ops[op](args);
    process.stdout.write(JSON.stringify({ id, ok: true, value: value ?? null }) + "\n");
  } catch (error) {
    process.stdout.write(JSON.stringify({ id, ok: false, message: String(error?.stack ?? error) }) + "\n");
  }
});
lines.on("close", () => { void ops.quit(); });
process.stdout.write(JSON.stringify({ ready: true }) + "\n");
