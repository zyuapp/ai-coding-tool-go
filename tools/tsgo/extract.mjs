// Describes every exported type in src/{domain,contracts,application,host} as a JSON tree for main.go.
//   node extract.mjs <ai-coding-tool/src>
import path from "node:path";
import fs from "node:fs";
import ts from "typescript";

const src = path.resolve(process.argv[2]);
const LAYERS = ["domain", "contracts", "application", "host"];

function walk(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) return walk(full);
    return entry.name.endsWith(".ts") && !entry.name.endsWith(".d.ts") ? [full] : [];
  });
}

const roots = LAYERS.flatMap((layer) => walk(path.join(src, layer))).sort();
const options = {
  target: ts.ScriptTarget.ES2022,
  module: ts.ModuleKind.NodeNext,
  moduleResolution: ts.ModuleResolutionKind.NodeNext,
  strict: true,
  skipLibCheck: true,
  noEmit: true,
  types: [],
};
// The app's own node_modules stay out: its packages' types never reach an exported type here.
const theirs = path.join(path.dirname(src), "node_modules") + path.sep;
const host = ts.createCompilerHost(options);
const fileExists = host.fileExists.bind(host);
const directoryExists = host.directoryExists?.bind(host);
host.fileExists = (file) => !path.resolve(file).startsWith(theirs) && fileExists(file);
host.directoryExists = (dir) => !(path.resolve(dir) + path.sep).startsWith(theirs) && (directoryExists ? directoryExists(dir) : true);
const program = ts.createProgram(roots, options, host);
const checker = program.getTypeChecker();

const inLayers = (file) => {
  const rel = path.relative(src, file);
  return !rel.startsWith("..") && LAYERS.includes(rel.split(path.sep)[0]);
};
const relFile = (file) => path.relative(src, file).split(path.sep).join("/");

class NeedsChecker extends Error {}

// Keeps the comment written above a union member on the object it describes.
function withDoc(converted, node, from) {
  if (converted.k !== "object" && converted.k !== "union") return converted;
  const docs = node.getSourceFile().text.slice(from, node.getStart()).match(/\/\*\*[\s\S]*?\*\//g);
  if (!docs) return converted;
  const doc = docs[docs.length - 1].replace(/^\/\*\*\s?/, "").replace(/\s*\*\/$/, "").split("\n").map((line) => line.replace(/^\s*\* ?/, "").trimEnd()).join("\n").trim();
  return doc ? { ...converted, doc } : converted;
}

// Names a type literal by where it is written, so a copy the checker hands back (from Extract, say) is the same Go type.
function withId(object, node) {
  if (object.k !== "object") return object;
  for (let at = node.parent; at; at = at.parent) {
    if ((ts.isTypeAliasDeclaration(at) || ts.isInterfaceDeclaration(at) || ts.isFunctionLike(at) || ts.isMappedTypeNode(at)) && (at.typeParameters?.length || ts.isMappedTypeNode(at))) return object;
  }
  object.id = `${relFile(node.getSourceFile().fileName)}:${node.getStart()}`;
  return object;
}

// Declarations reached from exported types, keyed "file#name"; local ones are emitted too.
const wanted = new Map();
const problems = [];

function declOf(symbol) {
  if (!symbol) return undefined;
  if (symbol.flags & ts.SymbolFlags.Alias) symbol = checker.getAliasedSymbol(symbol);
  const decl = symbol.declarations?.find((d) => ts.isTypeAliasDeclaration(d) || ts.isInterfaceDeclaration(d));
  if (!decl || !inLayers(decl.getSourceFile().fileName)) return undefined;
  return decl;
}

function refTo(decl) {
  const file = relFile(decl.getSourceFile().fileName);
  const name = decl.name.text;
  const key = `${file}#${name}`;
  if (!wanted.has(key)) wanted.set(key, decl);
  return { k: "ref", name, file };
}

function doc(symbolOrNode) {
  if (!symbolOrNode) return undefined;
  const parts = symbolOrNode.getDocumentationComment ? symbolOrNode.getDocumentationComment(checker) : [];
  const text = ts.displayPartsToString(parts).trim();
  return text || undefined;
}

const prim = (t) => ({ k: "prim", t });

function literalOfNode(node) {
  const lit = node.literal;
  switch (lit.kind) {
    case ts.SyntaxKind.StringLiteral: case ts.SyntaxKind.NoSubstitutionTemplateLiteral: return { k: "lit", v: lit.text };
    case ts.SyntaxKind.NumericLiteral: return { k: "lit", v: Number(lit.text) };
    case ts.SyntaxKind.TrueKeyword: return { k: "lit", v: true };
    case ts.SyntaxKind.FalseKeyword: return { k: "lit", v: false };
    case ts.SyntaxKind.NullKeyword: return prim("null");
    case ts.SyntaxKind.PrefixUnaryExpression: return { k: "lit", v: -Number(lit.operand.text) };
    default: throw new NeedsChecker();
  }
}

function fromNode(node, where) {
  switch (node.kind) {
    case ts.SyntaxKind.ParenthesizedType: return fromNode(node.type, where);
    case ts.SyntaxKind.StringKeyword: return prim("string");
    case ts.SyntaxKind.NumberKeyword: return prim("number");
    case ts.SyntaxKind.BooleanKeyword: return prim("boolean");
    case ts.SyntaxKind.UnknownKeyword: case ts.SyntaxKind.AnyKeyword: case ts.SyntaxKind.ObjectKeyword: return prim("unknown");
    case ts.SyntaxKind.UndefinedKeyword: case ts.SyntaxKind.VoidKeyword: return prim("undefined");
    case ts.SyntaxKind.NeverKeyword: return prim("never");
    case ts.SyntaxKind.BigIntKeyword: return prim("number");
    case ts.SyntaxKind.LiteralType: return literalOfNode(node);
    case ts.SyntaxKind.TemplateLiteralType: return prim("string");
    case ts.SyntaxKind.ArrayType: return { k: "array", e: fromNode(node.elementType, where) };
    case ts.SyntaxKind.TupleType: return { k: "tuple", es: node.elements.map((e) => fromNode(ts.isNamedTupleMember(e) ? e.type : e, where)) };
    case ts.SyntaxKind.TypeOperator:
      if (node.operator === ts.SyntaxKind.ReadonlyKeyword) return fromNode(node.type, where);
      throw new NeedsChecker();
    case ts.SyntaxKind.UnionType: return { k: "union", ms: node.types.map((t, i) => withDoc(fromNode(t, where), t, i ? node.types[i - 1].end : node.pos)) };
    case ts.SyntaxKind.IntersectionType: return intersection(node, where);
    case ts.SyntaxKind.TypeLiteral: return withId(objectFromMembers(node.members, where), node);
    case ts.SyntaxKind.FunctionType: return funcFromSignature(checker.getSignaturesOfType(checker.getTypeFromTypeNode(node), ts.SignatureKind.Call)[0], where);
    case ts.SyntaxKind.TypePredicate: return prim("boolean");
    case ts.SyntaxKind.TypeReference: case ts.SyntaxKind.ImportType: return reference(node, where);
    default: throw new NeedsChecker();
  }
}

const BUILTIN_EXTERNAL = new Set(["AbortSignal", "Error", "Date", "RegExp", "Uint8Array", "Buffer"]);

function reference(node, where) {
  const type = checker.getTypeFromTypeNode(node);
  let symbol = ts.isTypeReferenceNode(node) ? checker.getSymbolAtLocation(node.typeName) : type.aliasSymbol ?? type.symbol;
  if (symbol && symbol.flags & ts.SymbolFlags.Alias) symbol = checker.getAliasedSymbol(symbol);
  if (!symbol || symbol.flags & ts.SymbolFlags.TypeParameter) throw new NeedsChecker();
  const args = node.typeArguments ?? [];
  const decl = declOf(symbol);
  if (decl) {
    if (decl.typeParameters?.length) return fromType(type, where);
    return refTo(decl);
  }
  const name = symbol.name;
  switch (name) {
    case "Array": case "ReadonlyArray": return { k: "array", e: fromNode(args[0], where) };
    case "Record": return { k: "map", key: fromNode(args[0], where), val: fromNode(args[1], where) };
    case "Promise": return { k: "promise", e: fromNode(args[0], where) };
    case "Set": case "ReadonlySet": return { k: "set", e: fromNode(args[0], where) };
    case "Map": case "ReadonlyMap": return { k: "jsmap", key: fromNode(args[0], where), val: fromNode(args[1], where) };
  }
  if (BUILTIN_EXTERNAL.has(name) || !inLayers(symbol.declarations?.[0]?.getSourceFile().fileName ?? "/")) {
    if (["Partial", "Required", "Readonly", "Pick", "Omit", "Extract", "Exclude", "NonNullable", "ReturnType", "Parameters", "Awaited", "Uppercase", "Lowercase"].includes(name)) return fromType(type, where);
    return { k: "external", name };
  }
  return fromType(type, where);
}

function propNames(type) {
  return checker.getPropertiesOfType(type).map((p) => p.name);
}

function intersection(node, where) {
  const whole = checker.getTypeFromTypeNode(node);
  if (whole.flags & ts.TypeFlags.Union) return fromType(whole, where, { structural: true });
  const prims = node.types.map((t) => checker.getTypeFromTypeNode(t)).filter((t) => t.flags & (ts.TypeFlags.StringLike | ts.TypeFlags.NumberLike | ts.TypeFlags.BooleanLike));
  if (prims.length) return fromType(prims[0], where);
  const embeds = [];
  const own = [];
  const seen = new Set();
  let simple = true;
  for (const part of node.types) {
    const inner = ts.isParenthesizedTypeNode(part) ? part.type : part;
    const partType = checker.getTypeFromTypeNode(inner);
    const names = propNames(partType);
    if (names.some((n) => seen.has(n))) { simple = false; break; }
    names.forEach((n) => seen.add(n));
    if (ts.isTypeLiteralNode(inner)) { own.push(inner); continue; }
    if (ts.isTypeReferenceNode(inner) || ts.isImportTypeNode(inner)) {
      const symbol = ts.isTypeReferenceNode(inner) ? checker.getSymbolAtLocation(inner.typeName) : partType.aliasSymbol;
      const decl = declOf(symbol);
      if (decl && !decl.typeParameters?.length && isObjectDecl(decl) && !checker.getIndexInfosOfType(partType).length) { embeds.push(refTo(decl)); continue; }
    }
    simple = false;
    break;
  }
  if (!simple) return fromType(whole, where, { structural: true });
  const object = objectFromMembers(own.flatMap((n) => [...n.members]), where);
  object.embeds = embeds;
  return object;
}

function isObjectDecl(decl) {
  if (ts.isInterfaceDeclaration(decl)) return true;
  const type = checker.getTypeAtLocation(decl.name);
  if (type.flags & (ts.TypeFlags.Union | ts.TypeFlags.Primitive)) return false;
  return !!(type.flags & (ts.TypeFlags.Object | ts.TypeFlags.Intersection)) && !checker.getSignaturesOfType(type, ts.SignatureKind.Call).length;
}

function objectFromMembers(members, where) {
  const props = [];
  let index;
  let call;
  for (const member of members) {
    if (ts.isPropertySignature(member)) {
      const symbol = checker.getSymbolAtLocation(member.name);
      props.push(propOf(symbol, `${where}.${propName(member.name)}`, member));
    } else if (ts.isMethodSignature(member)) {
      const symbol = checker.getSymbolAtLocation(member.name);
      const signature = checker.getSignatureFromDeclaration(member);
      props.push({ name: propName(member.name), opt: !!member.questionToken, method: true, doc: doc(symbol), t: funcFromSignature(signature, `${where}.${propName(member.name)}`) });
    } else if (ts.isIndexSignatureDeclaration(member)) {
      index = { key: fromNode(member.parameters[0].type, where), val: guard(() => fromNode(member.type, where), () => fromType(checker.getTypeFromTypeNode(member.type), where)) };
    } else if (ts.isCallSignatureDeclaration(member)) {
      call = funcFromSignature(checker.getSignatureFromDeclaration(member), where);
    } else {
      problems.push(`${where}: unsupported member ${ts.SyntaxKind[member.kind]}`);
    }
  }
  if (call && !props.length) return call;
  if (index && !props.length) return { k: "map", key: index.key, val: index.val };
  if (index) problems.push(`${where}: index signature beside properties`);
  return { k: "object", props };
}

function propName(name) {
  if (ts.isIdentifier(name) || ts.isStringLiteral(name) || ts.isNumericLiteral(name) || ts.isPrivateIdentifier(name)) return name.text;
  return name.getText();
}

function guard(first, fallback) {
  try { return first(); } catch (error) { if (error instanceof NeedsChecker) return fallback(); throw error; }
}

function propOf(symbol, where, node) {
  const decl = node ?? symbol.valueDeclaration ?? symbol.declarations?.[0];
  const opt = !!(symbol.flags & ts.SymbolFlags.Optional);
  if (decl && (ts.isMethodSignature(decl) || ts.isMethodDeclaration(decl))) {
    const signature = checker.getSignaturesOfType(checker.getTypeOfSymbol(symbol), ts.SignatureKind.Call)[0];
    return { name: symbol.name, opt, method: true, doc: doc(symbol), t: funcFromSignature(signature, where) };
  }
  const viaChecker = () => {
    let type = checker.getTypeOfSymbol(symbol);
    return fromType(type, where);
  };
  const t = decl && decl.type && !symbol.links?.mappedType && !(symbol.checkFlags & 16384) ? guard(() => fromNode(decl.type, where), viaChecker) : viaChecker();
  return { name: symbol.name, opt, doc: doc(symbol), t };
}

function funcFromSignature(signature, where) {
  if (!signature) return { k: "func", params: [], ret: prim("undefined") };
  const params = signature.getParameters().map((param) => {
    const decl = param.valueDeclaration;
    const t = decl && decl.type ? guard(() => fromNode(decl.type, where), () => fromType(checker.getTypeOfSymbol(param), where)) : fromType(checker.getTypeOfSymbol(param), where);
    return { name: param.name, opt: !!(decl && (decl.questionToken || decl.initializer)), t };
  });
  const decl = signature.getDeclaration();
  const ret = decl && decl.type ? guard(() => fromNode(decl.type, where), () => fromType(signature.getReturnType(), where)) : fromType(signature.getReturnType(), where);
  return { k: "func", params, ret };
}

const NAMED_EXTERNAL = new Map([["Promise", "promise"], ["Set", "set"], ["ReadonlySet", "set"], ["Map", "jsmap"], ["ReadonlyMap", "jsmap"]]);

function fromType(type, where, { structural = false } = {}) {
  if (!structural && type.aliasSymbol) {
    const decl = declOf(type.aliasSymbol);
    if (decl && !type.aliasTypeArguments?.length) return refTo(decl);
  }
  const f = type.flags;
  if (f & (ts.TypeFlags.Any | ts.TypeFlags.Unknown | ts.TypeFlags.NonPrimitive)) return prim("unknown");
  if (f & ts.TypeFlags.Boolean) return prim("boolean");
  if (f & ts.TypeFlags.BooleanLiteral) return { k: "lit", v: type.intrinsicName === "true" };
  if (f & ts.TypeFlags.StringLiteral) return { k: "lit", v: type.value };
  if (f & ts.TypeFlags.NumberLiteral) return { k: "lit", v: type.value };
  if (f & (ts.TypeFlags.String | ts.TypeFlags.TemplateLiteral | ts.TypeFlags.StringMapping)) return prim("string");
  if (f & (ts.TypeFlags.Number | ts.TypeFlags.BigInt | ts.TypeFlags.BigIntLiteral)) return prim("number");
  if (f & ts.TypeFlags.Null) return prim("null");
  if (f & (ts.TypeFlags.Undefined | ts.TypeFlags.Void)) return prim("undefined");
  if (f & ts.TypeFlags.Never) return prim("never");
  if (f & ts.TypeFlags.TypeParameter) {
    const constraint = checker.getBaseConstraintOfType(type);
    return constraint && constraint !== type ? fromType(constraint, where) : prim("unknown");
  }
  if (f & ts.TypeFlags.Union) {
    const source = !structural && type.origin && type.origin.flags & ts.TypeFlags.Union ? type.origin.types : type.types;
    const members = [];
    let trues = 0;
    for (const member of source) {
      if (member.flags & ts.TypeFlags.BooleanLiteral) { trues++; }
      members.push(fromType(member, where));
    }
    const hasTrue = members.some((m) => m.k === "lit" && m.v === true), hasFalse = members.some((m) => m.k === "lit" && m.v === false);
    if (hasTrue && hasFalse) return { k: "union", ms: [...members.filter((m) => !(m.k === "lit" && typeof m.v === "boolean")), prim("boolean")] };
    return { k: "union", ms: members };
  }
  if (f & ts.TypeFlags.Intersection) {
    const prims = type.types.filter((t) => t.flags & (ts.TypeFlags.StringLike | ts.TypeFlags.NumberLike | ts.TypeFlags.BooleanLike));
    if (prims.length) return fromType(prims[0], where);
    return objectFromType(type, where);
  }
  if (f & ts.TypeFlags.Index) return prim("string");
  if (f & ts.TypeFlags.Object) {
    if (checker.isArrayType(type)) return { k: "array", e: fromType(checker.getTypeArguments(type)[0], where) };
    if (checker.isTupleType(type)) return { k: "tuple", es: checker.getTypeArguments(type).map((t) => fromType(t, where)) };
    const symbol = type.symbol;
    if (symbol && NAMED_EXTERNAL.has(symbol.name) && !inLayers(symbol.declarations?.[0]?.getSourceFile().fileName ?? "/")) {
      const args = checker.getTypeArguments(type);
      const kind = NAMED_EXTERNAL.get(symbol.name);
      if (kind === "promise") return { k: "promise", e: fromType(args[0], where) };
      if (kind === "set") return { k: "set", e: fromType(args[0], where) };
      return { k: "jsmap", key: fromType(args[0], where), val: fromType(args[1], where) };
    }
    if (!structural && symbol && type.objectFlags & ts.ObjectFlags.Interface) {
      const decl = declOf(symbol);
      if (decl && !decl.typeParameters?.length) return refTo(decl);
    }
    if (symbol && !inLayers(symbol.declarations?.[0]?.getSourceFile().fileName ?? "/") && symbol.flags & (ts.SymbolFlags.Interface | ts.SymbolFlags.Class)) return { k: "external", name: symbol.name };
    return objectFromType(type, where);
  }
  problems.push(`${where}: unsupported type ${checker.typeToString(type)}`);
  return prim("unknown");
}

function objectFromType(type, where) {
  const calls = checker.getSignaturesOfType(type, ts.SignatureKind.Call);
  const props = checker.getPropertiesOfType(type);
  if (calls.length && !props.length) return funcFromSignature(calls[0], where);
  const indexes = checker.getIndexInfosOfType(type);
  // A record over a union of known keys and any string keeps the known ones as properties; it is still a record.
  if (indexes.length) return { k: "map", key: fromType(indexes[0].keyType, where), val: fromType(indexes[0].type, where) };
  const literal = type.symbol?.declarations?.[0];
  const object = { k: "object", props: props.map((p) => propOf(p, `${where}.${p.name}`)) };
  return literal && ts.isTypeLiteralNode(literal) && type.objectFlags & ts.ObjectFlags.Anonymous ? withId(object, literal) : object;
}

function convertDecl(decl) {
  const name = decl.name.text;
  const file = relFile(decl.getSourceFile().fileName);
  const where = `${file}#${name}`;
  const symbol = checker.getSymbolAtLocation(decl.name);
  const out = { name, file, pos: decl.getStart(), exported: !!decl.modifiers?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword), doc: doc(symbol) };
  if (decl.typeParameters?.length) return { ...out, generic: decl.typeParameters.map((p) => p.name.text), t: prim("unknown") };
  if (ts.isInterfaceDeclaration(decl)) {
    const heritage = (decl.heritageClauses ?? []).flatMap((c) => [...c.types]);
    const declared = checker.getDeclaredTypeOfSymbol(symbol);
    const embeds = [];
    let simple = true;
    const seen = new Set();
    for (const base of heritage) {
      const baseSymbol = checker.getSymbolAtLocation(base.expression);
      const baseDecl = declOf(baseSymbol);
      if (!baseDecl || base.typeArguments?.length || baseDecl.typeParameters?.length || !isObjectDecl(baseDecl)) { simple = false; break; }
      const names = propNames(checker.getTypeAtLocation(base));
      if (names.some((n) => seen.has(n))) { simple = false; break; }
      names.forEach((n) => seen.add(n));
      embeds.push(refTo(baseDecl));
    }
    const own = decl.members.filter((m) => !(m.name && seen.has(propName(m.name))));
    if (simple && own.length === decl.members.length) {
      const object = objectFromMembers(decl.members, where);
      if (embeds.length) object.embeds = embeds;
      return { ...out, t: object };
    }
    return { ...out, t: objectFromType(declared, where) };
  }
  const t = guard(() => {
    if (ts.isTypeReferenceNode(decl.type) || ts.isImportTypeNode(decl.type)) return reference(decl.type, where);
    return fromNode(decl.type, where);
  }, () => fromType(checker.getTypeAtLocation(decl.name), where, { structural: true }));
  if (t.k === "ref" && t.name === name && t.file === file) return { ...out, t: fromType(checker.getTypeAtLocation(decl.name), where, { structural: true }) };
  return { ...out, t };
}

const decls = [];
for (const file of program.getSourceFiles()) {
  if (!inLayers(file.fileName)) continue;
  for (const statement of file.statements) {
    if ((ts.isTypeAliasDeclaration(statement) || ts.isInterfaceDeclaration(statement)) && statement.modifiers?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword)) {
      refTo(statement);
    }
  }
}
const done = new Set();
let progress = true;
while (progress) {
  progress = false;
  for (const [key, decl] of [...wanted]) {
    if (done.has(key)) continue;
    done.add(key);
    progress = true;
    decls.push(convertDecl(decl));
  }
}
const diagnostics = ts.getPreEmitDiagnostics(program).filter((d) => d.category === ts.DiagnosticCategory.Error).length;
decls.sort((a, b) => (a.file === b.file ? a.pos - b.pos : a.file < b.file ? -1 : 1));
process.stdout.write(JSON.stringify({ decls, problems, diagnostics }));
