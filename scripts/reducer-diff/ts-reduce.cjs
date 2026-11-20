// The TypeScript reducer over stdin, with ids and the clock pinned as in cmd/reduce-lines.
//   node ts-reduce.cjs <dist>   (dist holds main/application/workspace-reducer.js)
// `{"views": {at, taskId, before, reference}}` answers the projections the host makes of the current state;
// `patches` and `mobilePatch` run from the state and mobile view of the previous views request.
const dist = process.argv[2];
const readline = require("node:readline");
let fixedNow = 0;
let ids = 0;
Date.now = () => fixedNow;
const pad = (n) => String(n).padStart(12, "0");
globalThis.crypto.randomUUID = () => `00000000-0000-4000-8000-${pad(ids++)}`;
const { reduce } = require(`${dist}/main/application/workspace-reducer.js`);
const { emptyWorkspaceState } = require(`${dist}/main/application/workspace-state.js`);
const app = (name) => require(`${dist}/main/application/${name}.js`);
const mobile = app("mobile-projection");
const { threadLists } = app("thread-activity");
const { remoteUnreadCount } = app("computers");
const projection = app("thread-projection");
const { workspacePatches } = app("workspace-patches");
let state = emptyWorkspaceState();
let viewed = { state, mobile: null };
const plain = (key, value) => value instanceof Set ? [...value] : value instanceof Map ? Object.fromEntries(value) : value;
const rl = readline.createInterface({ input: process.stdin });
rl.on("line", (line) => {
  let asked;
  try { asked = JSON.parse(line); } catch { console.log(JSON.stringify({ error: "unreadable line" })); return; }
  if (asked.reset) { state = emptyWorkspaceState(); viewed = { state, mobile: null }; ids = 0; console.log(JSON.stringify({ state }, plain)); return; }
  if (asked.views) {
    try { console.log(JSON.stringify({ views: views(asked.views) }, plain)); } catch (error) { console.log(JSON.stringify({ error: `views threw: ${error && error.stack || error}` })); }
    return;
  }
  if (asked.state) { state = asked.state; ids = 0; }
  fixedNow = asked.now ?? 0;
  try {
    const transition = reduce(state, asked.input);
    state = transition.state;
    console.log(JSON.stringify({ state, effects: transition.effects, result: transition.result, ids }, plain));
  } catch (error) {
    console.log(JSON.stringify({ error: `threw: ${error && error.stack || error}` }));
  }
});

function views({ at, taskId, before, reference }) {
  fixedNow = at;
  const ids = (threads) => threads.map((thread) => thread.id);
  const lists = threadLists(state);
  const view = mobile.projectMobileView(state, at);
  const answer = {
    mobile: view,
    mobilePatch: viewed.mobile ? mobile.diffMobileView(viewed.mobile, view) : null,
    archived: mobile.mobileArchivedThreads(state),
    read: mobile.mobileThreadRead(state, taskId),
    history: mobile.mobileThreadHistory(state, taskId, before),
    lists: { listed: ids(lists.listedThreads), visible: ids(lists.visibleThreads), archived: ids(lists.archivedThreads), worktreeThreadIds: [...lists.worktreeThreadIds].sort(), sideChatAttention: [...lists.sideChatAttention].sort(), unreadCount: lists.unreadCount },
    badge: lists.unreadCount + remoteUnreadCount(state.computers),
    summaries: [
      projection.threadSummaries(state, { scope: { kind: "all" } }, at),
      projection.threadSummaries(state, { scope: { kind: "all" }, archived: true, search: reference, limit: 5 }, at),
      projection.threadSummaries(state, { scope: { kind: "project", projectId: "project-1" }, idleForMs: 1000 }, at),
      projection.threadSummaries(state, { scope: { kind: "projectless" }, attachments: true }, at),
    ],
    transcript: projection.threadTranscript(state, taskId),
    wait: projection.threadWaitResult(state, taskId, false),
    find: projection.findThread(state, reference)?.id ?? null,
    patches: workspacePatches(viewed.state, state),
  };
  viewed = { state, mobile: view };
  return answer;
}
