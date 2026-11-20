// Every member of a Go union, read from its jsonx.RegisterUnion call in the generated types:
//   universe("internal/application/workspace_reducer_types_types.go", "WorkspaceInput") -> ["task.new", ...]
const fs = require("node:fs");
const path = require("node:path");
const root = path.resolve(__dirname, "../..");
module.exports = function universe(file, union) {
  const text = fs.readFileSync(path.join(root, file), "utf8");
  const start = text.indexOf(`jsonx.RegisterUnion[${union}](`);
  if (start < 0) throw new Error(`no ${union} in ${file}`);
  const end = text.indexOf("\n\t})", start);
  return [...text.slice(start, end).matchAll(/^\t\t"([^"]+)":/gm)].map((match) => match[1]);
};
