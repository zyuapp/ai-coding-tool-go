// Captures what the TypeScript tool servers list and how they parse arguments, for schema_test.go.
// Run a copy of it with node from a folder whose node_modules holds the app's zod and @modelcontextprotocol/sdk,
// with TOOLS_DIST naming the compiled src/main/tools folder:
//   TOOLS_DIST=<dist>/main/tools node <folder>/capture.mjs <testdata folder>
// It writes tools_list.json and zod_corpus.jsonl.gz there.
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { gzipSync } from "node:zlib";
import { z } from "zod";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
const D = process.env.TOOLS_DIST.replace(/\/?$/, "/");
const out = process.argv[2];
const { threadTools } = await import(D + "threads.mjs");
const { coordinationTools } = await import(D + "coordination.mjs");
const { automationTools, findingTools } = await import(D + "automation.mjs");
const { browserTools } = await import(D + "browser.mjs");
const { simulatorTools } = await import(D + "simulator.mjs");
const { terminalTools } = await import(D + "terminal.mjs");
const { computerUseSetupTools } = await import(D + "computer-use.mjs");
const nop = new Proxy({}, { get: () => async () => { throw new Error("nop"); } });
const sets = {
  threads: threadTools(nop, () => 0),
  threadsCoordinator: threadTools(nop, () => 0, "coordinator"),
  threadsMember: threadTools(nop, () => 0, "member"),
  coordinationCoordinator: coordinationTools(nop, "coordinator"),
  coordinationMember: coordinationTools(nop, "member"),
  automation: automationTools(nop),
  findings: findingTools(nop),
  browser: browserTools(nop),
  simulator: simulatorTools(nop),
  terminal: terminalTools(nop),
};
const LONG = 1_000_001;
const rep = (n, c = "x") => ({ $repeat: c, n });
const expand = (v) => Array.isArray(v) ? v.map(expand) : v && typeof v === "object" ? ("$repeat" in v ? v.$repeat.repeat(v.n) : Object.fromEntries(Object.entries(v).map(([k, x]) => [k, expand(x)]))) : v;
const norm = (v) => typeof v === "string" ? (v.length > 80 ? `<${v.length} chars>` : v) : Array.isArray(v) ? v.map(norm) : v && typeof v === "object" ? Object.fromEntries(Object.entries(v).map(([k, x]) => [k, norm(x)])) : v;

function inner(s) {
  if (s instanceof z.ZodPipe) return inner(s.in);
  return s instanceof z.ZodOptional || s instanceof z.ZodNullable ? inner(s.unwrap()) : s;
}
function sample(s) {
  const f = inner(s);
  if (f instanceof z.ZodString) return f.minLength && f.minLength > 1 ? rep(f.minLength) : "x";
  if (f instanceof z.ZodNumber) return 1;
  if (f instanceof z.ZodBoolean) return true;
  if (f instanceof z.ZodEnum) return f.options[0];
  if (f instanceof z.ZodArray) return [sample(f.element)];
  if (f instanceof z.ZodObject) return Object.fromEntries(Object.entries(f.shape).map(([k, v]) => [k, sample(v)]));
  throw new Error("no sample");
}
function edges(s) {
  const f = inner(s);
  const out = [null, 7, "str", true, [], {}];
  if (f instanceof z.ZodString) {
    const max = f.maxLength ?? 2000;
    out.push("", " ", " \n\t", "  ﻿", "  pad  ", "a b  c", rep(max), rep(max + 1), rep(max - 1), { $repeat: "😀", n: Math.ceil(max / 2) + 1 }, { $repeat: "😀", n: Math.floor(max / 2) }, "GLM 5.3 Flash", "  sonnet ", "bad id!", "constructor", "gpt-6-luna", "claude", "new", rep(LONG));
  }
  if (f instanceof z.ZodNumber) out.push(-1, 0, 0.5, 1, 49, 50, 199, 200, 201, 4000, 5000, 5001, 9999, 10000, 10001, 1e12, 1e20, -0.5, 9007199254740992);
  if (f instanceof z.ZodBoolean) out.push(false, "true", 0);
  if (f instanceof z.ZodEnum) out.push(...f.options, "nope", f.options[0].toUpperCase());
  if (f instanceof z.ZodArray) out.push([sample(f.element), sample(f.element)], Array.from({ length: 13 }, () => sample(f.element)), Array.from({ length: 6 }, () => sample(f.element)), ["x"], [null]);
  return out;
}
function cases(shape) {
  const base = sample(z.object(shape));
  const out = [{}, base, { ...base, extra: 1, __proto__x: 2 }];
  const keys = Object.keys(shape);
  // all wrong
  out.push(Object.fromEntries(keys.map((k) => [k, 7])));
  out.push(Object.fromEntries(keys.map((k) => [k, null])));
  for (const [key, value] of Object.entries(shape)) {
    const rest = { ...base }; delete rest[key]; out.push(rest);
    for (const edge of edges(value)) out.push({ ...base, [key]: edge });
    const f = inner(value);
    if (f instanceof z.ZodArray && inner(f.element) instanceof z.ZodObject) {
      const item = inner(f.element);
      const first = base[key][0];
      for (const [nested, schema] of Object.entries(item.shape)) {
        const r = { ...first }; delete r[nested];
        out.push({ ...base, [key]: [r] });
        for (const edge of edges(schema)) out.push({ ...base, [key]: [{ ...first, [nested]: edge }] });
        out.push({ ...base, [key]: [{ ...first, [nested]: 5 }, { ...first, [nested]: 6 }] });
      }
      if ("label" in item.shape) {
        out.push({ ...base, [key]: [{ label: "Keep it" }, { label: " keep IT " }] });
        out.push({ ...base, [key]: [{ label: "A" }, { label: "B", description: " ", recommended: false }, { label: "a" }] });
        out.push({ ...base, [key]: [{ label: "" }, { label: "" }] });
      }
    }
  }
  return out;
}
const corpus = [];
for (const [set, tools] of Object.entries(sets)) {
  const server = new McpServer({ name: "aicodingtool", version: "1.0.0" });
  for (const bound of tools) server.registerTool(bound.name, { description: bound.description, inputSchema: bound.input }, (args) => ({ content: [{ type: "text", text: JSON.stringify(norm(args)) }] }));
  const [c, s] = InMemoryTransport.createLinkedPair();
  await server.connect(s);
  const client = new Client({ name: "t", version: "1" });
  await client.connect(c);
  for (const bound of tools) {
    const seen = new Set();
    for (const args of cases(bound.input)) {
      const key = JSON.stringify(args);
      if (seen.has(key)) continue;
      seen.add(key);
      const result = await client.callTool({ name: bound.name, arguments: expand(args) });
      corpus.push({ set, tool: bound.name, args, text: result.content[0].text, isError: result.isError ?? false });
    }
  }
  const result = await client.callTool({ name: "no_such_tool", arguments: {} });
  corpus.push({ set, tool: "no_such_tool", args: {}, text: result.content[0].text, isError: result.isError ?? false });
  await client.close();
}

const listedSets = {
  ...sets,
  automationCoordinator: automationTools(nop, "coordinator"),
  browserReadOnly: browserTools(nop, { readOnly: true }),
  simulatorReadOnly: simulatorTools(nop, { readOnly: true }),
  computerUseSetup: computerUseSetupTools({ requestSetup() {} }),
};
const lists = {};
for (const [set, tools] of Object.entries(listedSets)) {
  const server = new McpServer({ name: "aicodingtool", version: "1.0.0" });
  for (const bound of tools) server.registerTool(bound.name, { description: bound.description, inputSchema: bound.input }, (args) => bound.handler(args));
  const [c, s] = InMemoryTransport.createLinkedPair();
  await server.connect(s);
  const client = new Client({ name: "t", version: "1" });
  await client.connect(c);
  lists[set] = (await client.listTools()).tools.map((tool) => ({ name: tool.name, description: tool.description, inputSchema: JSON.stringify(tool.inputSchema), execution: tool.execution }));
  await client.close();
}
writeFileSync(join(out, "tools_list.json"), JSON.stringify(lists, null, 1) + "\n");
writeFileSync(join(out, "zod_corpus.jsonl.gz"), gzipSync(corpus.map((c) => JSON.stringify(c)).join("\n") + "\n", { level: 9 }));
