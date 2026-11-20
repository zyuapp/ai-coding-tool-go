// Routes a copy of the TypeScript app's reducer and the projections the host makes through go-reduce.ts
// (shim.ts), so its test suite runs cmd/reduce-lines beside them. Usage: node patch.cjs <ts-copy>
const fs = require("node:fs");
const path = require("node:path");
const root = process.argv[2];
const app = path.join(root, "src/application");
fs.copyFileSync(path.join(__dirname, "shim.ts"), path.join(app, "go-reduce.ts"));
const file = path.join(app, "workspace-reducer.ts");
let text = fs.readFileSync(file, "utf8");
if (!text.includes("./go-reduce.js")) {
  const head = "export function reduce(state: WorkspaceState, input: WorkspaceInput): WorkspaceTransition {\n";
  if (!text.includes(head)) throw new Error("workspace-reducer.ts: reduce() moved");
  text = text.replace(head, head
    + '  if (process.env.ACT_GO === "diff") return differential(tsReduce, state, input);\n'
    + "  return tsReduce(state, input);\n}\n\n"
    + "function tsReduce(state: WorkspaceState, input: WorkspaceInput): WorkspaceTransition {\n");
  fs.writeFileSync(file, 'import { differential } from "./go-reduce.js";\n' + text);
}

// Each checked against reduce-lines' `call` of the same name.
const projections = {
  "mobile-projection.ts": ["projectMobileView", "mobileArchivedThreads", "mobileThreadRead", "mobileThreadHistory"],
  "thread-activity.ts": ["threadLists"],
  "computers.ts": ["remoteUnreadCount"],
  "thread-projection.ts": ["threadSummaries", "threadTranscript", "threadWaitResult", "findThread"],
};
for (const [name, functions] of Object.entries(projections)) {
  const target = path.join(app, name);
  let source = fs.readFileSync(target, "utf8");
  if (source.includes("./go-reduce.js")) continue;
  for (const fn of functions) {
    const declared = new RegExp(`export function ${fn}(<[^>]*>)?\\(`);
    const found = source.match(declared);
    if (!found) throw new Error(`${name}: ${fn} moved`);
    source = source.replace(declared, `function ts_${fn}${found[1] ?? ""}(`);
    source += `\nexport function ${fn}(...args: Parameters<typeof ts_${fn}>): ReturnType<typeof ts_${fn}> {\n`
      + `  if (process.env.ACT_GO === "diff") return differentialCall("${fn}", () => (ts_${fn} as any)(...args), args);\n`
      + `  return (ts_${fn} as any)(...args);\n}\n`;
  }
  fs.writeFileSync(target, 'import { differentialCall } from "./go-reduce.js";\n' + source);
}

// Validators at the app's boundaries, each checked with values near every one the tests hand it against reduce-lines' `check`.
const validators = {
  "mobile.ts": ["isMobileClientMessage", "isMobileQuery", "isMobileRequest", "isMobileResponse"],
  "computers.ts": ["isComputerClientRequest", "isComputerQuery", "isComputerServerMessage"],
  "thread-requests.ts": ["isThreadRequest", "isFindingReport"],
  "ipc.ts": ["isThreadResponse", "isRunCommand", "isInternalRunCommand", "isAutomationRequest", "isAutomationResponse", "isAutomationAck", "isRunEvent", "isBackgroundEvent",
    "isSubagentEvent", "isGoalEvent", "isWorkflowEvent", "isBrowserRead", "isSimulatorRead", "isTerminalRead", "isWindowTheme", "isBrowserAction", "isBrowserBounds"],
  "session-resources.ts": ["isSessionResourceMessage", "isTerminateSessionCommand", "isResourceStoppedEvent"],
  "thread-results.ts": ["isThreadSummary", "isThreadTranscript"],
  "glass.ts": ["isGlassRegions"],
  "agent-settings.ts": ["isAgentSettingsReloadEvent"],
  "terminal.ts": ["isTerminalOutputRead"],
  "view-commands.ts": ["isFindTarget"],
  "workspace-view-input.ts": ["isWorkspaceViewInput", "isExternalCommand", "isPhoneCommand"],
};
for (const [name, functions] of Object.entries(validators)) {
  const target = path.join(root, "src/contracts", name);
  let source = fs.readFileSync(target, "utf8");
  if (source.includes("go-reduce.js")) continue;
  for (const fn of functions) {
    const declared = new RegExp(`export function ${fn}\\(`);
    if (!declared.test(source)) throw new Error(`${name}: ${fn} moved`);
    source = source.replace(declared, `function ts_${fn}(`);
    source += `\nexport function ${fn}(...args: Parameters<typeof ts_${fn}>): any {\n`
      + `  if (process.env.ACT_GO === "diff") return differentialCheck("${fn}", ts_${fn} as any, args);\n`
      + `  return (ts_${fn} as any)(...args);\n}\n`;
  }
  fs.writeFileSync(target, 'import { differentialCheck } from "../application/go-reduce.js";\n' + source);
}
