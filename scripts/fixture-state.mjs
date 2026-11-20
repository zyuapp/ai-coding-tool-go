#!/usr/bin/env node
// Builds WorkspaceState fixtures for the fixture backend (ACT_FIXTURE_STATE) with the compiled
// TypeScript backend: the Electron dev profile's threads, read from a copy of its store, with the
// current thread's messages loaded, as the window first shows them. `state.json` is that state;
// `state-settings.json` is the same state with settings open.
//
// `--thread ID` shows that thread instead of the one the profile last showed.
//
// usage: node scripts/fixture-state.mjs [--tsdist DIR] [--profile DIR] [--out DIR] [--width PX] [--thread ID]
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { homedir, tmpdir } from "node:os";
import path from "node:path";
import { parseArgs } from "node:util";
import { pathToFileURL } from "node:url";

const { values } = parseArgs({
  options: {
    tsdist: { type: "string", default: "/tmp/goport/tsdist/main" },
    profile: { type: "string", default: path.join(homedir(), "Library/Application Support/AI Coding Tool Dev") },
    out: { type: "string", default: path.join(import.meta.dirname, "../tmp/fixture") },
    width: { type: "string", default: "1240" },
    thread: { type: "string" },
  },
});

const dist = path.resolve(values.tsdist);
const require = createRequire(path.join(dist, "index.js"));
const { reduce } = require("./application/workspace-reducer.js");
const { stateFromData } = require("./application/workspace-load.js");
const { emptyWorkspaceState } = require("./application/workspace-state.js");
const { loadViewPreferences } = require("./host/view-preferences-store.js");
const { createTaskStore } = require("./host/draft-persistence.js");
const { createJsonStorage } = require("./main/json-storage.js");

/** The profile is only read: its store and window storage are copied before anything opens them. */
const scratch = mkdtempSync(path.join(tmpdir(), "aicodingtool-fixture-"));
function copied(name) {
  const source = path.join(values.profile, name);
  if (!existsSync(source)) return null;
  const target = path.join(scratch, name);
  copyFileSync(source, target);
  for (const suffix of ["-wal", "-shm"]) if (existsSync(source + suffix)) copyFileSync(source + suffix, target + suffix);
  return target;
}

function apply(state, input) {
  return reduce(state, input).state;
}

async function build() {
  const storageFile = copied("window.v1.json") ?? path.join(scratch, "window.v1.json");
  const storage = createJsonStorage(storageFile);
  const viewport = Number(values.width);
  const loaded = createTaskStore(storage).load();
  let state = loaded.ok ? stateFromData(loaded.data) : emptyWorkspaceState(loaded.errors.join(" "));
  state = apply(state, { type: "preferences.loaded", preferences: loadViewPreferences(storage, viewport) });

  const databaseFile = copied("tasks.v3.sqlite");
  if (!databaseFile) {
    state = apply(state, { type: "store.absent" });
    return state;
  }
  const { TaskDatabase } = await import(pathToFileURL(path.join(dist, "main/task-database.mjs")).href);
  const database = new TaskDatabase(databaseFile);
  try {
    const data = database.load({ summariesOnly: true });
    state = data ? apply(state, { type: "store.loaded", data, hiddenTasks: data.hiddenTasks }) : apply(state, { type: "store.absent" });
    if (values.thread) state = apply(state, { type: "task.select", taskId: values.thread });
    if (state.currentId) {
      state = apply(state, { type: "store.thread-loaded", taskId: state.currentId, messages: database.loadThreadMessages(state.currentId) });
    }
  } finally {
    database.close?.();
  }
  return state;
}

try {
  const state = await build();
  mkdirSync(values.out, { recursive: true });
  const write = (name, value) => {
    const file = path.join(values.out, name);
    writeFileSync(file, JSON.stringify(value));
    console.log(`${file} (${value.threads.length} threads, ${value.projects.length} projects)`);
  };
  write("state.json", state);
  write("state-settings.json", apply(state, { type: "view.set-settings-open", open: true }));
} finally {
  rmSync(scratch, { recursive: true, force: true });
}
