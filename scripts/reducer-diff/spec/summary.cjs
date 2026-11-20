// Groups the divergences spec.sh logged by input and first differing path: node summary.cjs <log>
const fs = require("node:fs");
const lines = fs.existsSync(process.argv[2]) ? fs.readFileSync(process.argv[2], "utf8").trim().split("\n").filter(Boolean).map((line) => JSON.parse(line)) : [];
const groups = new Map();
const calls = lines.reduce((sum, entry) => sum + (entry.calls ?? 0), 0);
for (const entry of lines.filter((entry) => !entry.calls)) {
  const what = entry.error ? `error: ${entry.error.split("\n")[0].slice(0, 160)}` : entry.problems[0].replace(/\[\d+\]/g, "[]").replace(/: ts=.*/, "");
  const key = `${entry.kind} ${what}`;
  const group = groups.get(key) ?? { count: 0, example: entry };
  group.count++;
  groups.set(key, group);
}
for (const [key, { count, example }] of [...groups].sort((a, b) => b[1].count - a[1].count)) {
  console.log(`${count}\t${key}\n\t${example.file} > ${example.test}\n\t${(example.problems ?? [example.error]).slice(0, 3).join("\n\t").slice(0, 900)}`);
}
console.log(`${[...groups.values()].reduce((sum, group) => sum + group.count, 0)} divergent reduces in ${groups.size} groups, of ${calls} checked`);
