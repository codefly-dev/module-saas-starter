// workflow-yaml — a deliberately small reader for the block-YAML subset that
// GitHub workflow files are written in: block mappings, block sequences, block
// scalars, flow sequences, and plain or quoted scalars.
//
// It exists because the release-gate contract has to decide whether an
// artifact-writing job is dominated by the mandatory checks, and that decision
// must not depend on a dependency the gate job itself installs. Anything
// outside the supported subset throws instead of parsing to a guess: an
// unreadable workflow has to be a loud failure, never a silently empty
// dependency list that lets a publisher through.

const indentOf = (line) => /^ */.exec(line)[0].length;
const isBlank = (line) => /^\s*$/.test(line);
const isComment = (line) => /^\s*#/.test(line);

// Drop a trailing `# …` comment, honouring quotes so a `#` inside a scalar
// survives. Block-scalar bodies never reach here — they are read raw.
function stripComment(text) {
  let out = "";
  let quote = null;
  for (let i = 0; i < text.length; i += 1) {
    const ch = text[i];
    if (quote) {
      out += ch;
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      out += ch;
      continue;
    }
    if (ch === "#" && (i === 0 || /\s/.test(text[i - 1]))) break;
    out += ch;
  }
  return out.replace(/\s+$/, "");
}

function unquote(text) {
  if (text.length >= 2 && text[0] === '"' && text.at(-1) === '"') {
    return text.slice(1, -1).replace(/\\(.)/g, "$1");
  }
  if (text.length >= 2 && text[0] === "'" && text.at(-1) === "'") {
    return text.slice(1, -1).replace(/''/g, "'");
  }
  return text;
}

// Split a flow sequence body on top-level commas only, so `["v*", "a/v*"]`
// survives and a nested flow collection is rejected rather than mis-split.
function parseFlowSequence(text, lineNumber) {
  const body = text.slice(1, -1).trim();
  if (body === "") return [];
  const items = [];
  let current = "";
  let quote = null;
  for (const ch of body) {
    if (quote) {
      current += ch;
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      current += ch;
      continue;
    }
    if (ch === "[" || ch === "{") {
      throw new Error(`line ${lineNumber}: nested flow collections are not supported`);
    }
    if (ch === ",") {
      items.push(current.trim());
      current = "";
      continue;
    }
    current += ch;
  }
  items.push(current.trim());
  return items.map(unquote);
}

function scalar(text, lineNumber) {
  if (text === "" || text === "~" || text === "null") return null;
  if (text.startsWith("[")) {
    if (!text.endsWith("]")) throw new Error(`line ${lineNumber}: unterminated flow sequence`);
    return parseFlowSequence(text, lineNumber);
  }
  if (text.startsWith("{")) throw new Error(`line ${lineNumber}: flow mappings are not supported`);
  if (text.startsWith("&") || text.startsWith("*")) {
    throw new Error(`line ${lineNumber}: anchors and aliases are not supported`);
  }
  return unquote(text);
}

// `|`, `>`, and their chomping variants. The body is every following line
// indented past the key, blank lines included; the common indent is stripped.
function readBlockScalar(ctx, header, keyIndent, lineNumber) {
  const style = header[0];
  const chomp = header.slice(1);
  if (!["", "-", "+"].includes(chomp)) {
    throw new Error(`line ${lineNumber}: unsupported block scalar header "${header}"`);
  }
  const body = [];
  while (ctx.i < ctx.lines.length) {
    const line = ctx.lines[ctx.i];
    if (!isBlank(line) && indentOf(line) <= keyIndent) break;
    body.push(line);
    ctx.i += 1;
  }
  while (body.length && isBlank(body.at(-1))) body.pop();
  if (body.length === 0) return "";
  const contentIndent = Math.min(
    ...body.filter((line) => !isBlank(line)).map(indentOf),
  );
  const stripped = body.map((line) => (isBlank(line) ? "" : line.slice(contentIndent)));
  const text = style === ">" ? stripped.join(" ") : stripped.join("\n");
  return chomp === "-" ? text : `${text}\n`;
}

function skipIgnorable(ctx) {
  while (ctx.i < ctx.lines.length) {
    const line = ctx.lines[ctx.i];
    if (isBlank(line) || isComment(line)) ctx.i += 1;
    else break;
  }
}

function peekIndent(ctx) {
  skipIgnorable(ctx);
  return ctx.i < ctx.lines.length ? indentOf(ctx.lines[ctx.i]) : -1;
}

const KEY_RE = /^[^:]+:(?:\s|$)/;

function isSequenceItem(line) {
  const body = line.trimStart();
  return body === "-" || body.startsWith("- ");
}

function parseNode(ctx, indent) {
  skipIgnorable(ctx);
  if (ctx.i >= ctx.lines.length) return null;
  const line = ctx.lines[ctx.i];
  if (indentOf(line) < indent) return null;
  return isSequenceItem(line)
    ? parseSequence(ctx, indentOf(line))
    : parseMapping(ctx, indentOf(line));
}

function parseSequence(ctx, indent) {
  const items = [];
  while (ctx.i < ctx.lines.length) {
    skipIgnorable(ctx);
    if (ctx.i >= ctx.lines.length) break;
    const line = ctx.lines[ctx.i];
    const ind = indentOf(line);
    if (ind < indent) break;
    if (ind > indent || !isSequenceItem(line)) {
      throw new Error(`line ${ctx.number(ctx.i)}: expected a sequence item`);
    }
    const dashRest = line.slice(ind + 1);
    const contentIndent = ind + 1 + /^ */.exec(dashRest)[0].length;
    if (dashRest.trim() === "") {
      throw new Error(`line ${ctx.number(ctx.i)}: an empty sequence item is not supported`);
    }
    const content = stripComment(line.slice(contentIndent));
    if (!KEY_RE.test(content)) {
      items.push(scalar(content, ctx.number(ctx.i)));
      ctx.i += 1;
      continue;
    }
    // Re-present a mapping item as an ordinary node at its own column so the
    // item's first key and its continuation lines take one code path.
    ctx.lines[ctx.i] = " ".repeat(contentIndent) + line.slice(contentIndent);
    items.push(parseNode(ctx, contentIndent));
  }
  return items;
}

function parseMapping(ctx, indent) {
  const map = {};
  while (ctx.i < ctx.lines.length) {
    skipIgnorable(ctx);
    if (ctx.i >= ctx.lines.length) break;
    const line = ctx.lines[ctx.i];
    const ind = indentOf(line);
    if (ind < indent) break;
    const lineNumber = ctx.number(ctx.i);
    if (ind > indent) throw new Error(`line ${lineNumber}: unexpected indentation`);
    if (isSequenceItem(line)) break;
    const body = stripComment(line.slice(ind));
    const match = /^([^:]+):(?:\s+(.*))?$/.exec(body);
    if (!match) throw new Error(`line ${lineNumber}: expected "key: value"`);
    const key = unquote(match[1].trim());
    const rest = (match[2] ?? "").trim();
    ctx.i += 1;
    if (/^[|>][-+]?$/.test(rest)) {
      map[key] = readBlockScalar(ctx, rest, ind, lineNumber);
      continue;
    }
    if (rest !== "") {
      map[key] = scalar(rest, lineNumber);
      continue;
    }
    // A bare key owns whatever follows: a deeper block, or a sequence written
    // back at the key's own column.
    const childIndent = peekIndent(ctx);
    if (childIndent > ind) map[key] = parseNode(ctx, childIndent);
    else if (childIndent === ind && isSequenceItem(ctx.lines[ctx.i])) {
      map[key] = parseSequence(ctx, ind);
    } else map[key] = null;
  }
  return map;
}

export function parseWorkflowYaml(text) {
  const lines = text.split("\n");
  const ctx = { lines, i: 0, number: (index) => index + 1 };
  const document = parseNode(ctx, 0);
  skipIgnorable(ctx);
  if (ctx.i < ctx.lines.length) {
    throw new Error(`line ${ctx.number(ctx.i)}: trailing content after the document`);
  }
  return document ?? {};
}
