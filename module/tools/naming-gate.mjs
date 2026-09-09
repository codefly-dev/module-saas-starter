#!/usr/bin/env node
// naming-gate — enforce AGENTS.md §"Naming and confidentiality" mechanically.
//
// The rule has existed since the repo was created: everything here — docs, specs, code,
// comments, tests, fixtures — must use only generic placeholder names, and must never name a
// real customer, partner, employer, or downstream consumer of this module. The reason is
// architectural, not merely legal: this module sits BELOW its consumers in the dependency
// graph, so it must carry no build-time or documentation-level knowledge of who composes it.
//
// Nothing checked it, so it eroded. By the time this gate was written the tree carried ~380
// violating lines across 68 files: two consumer-premised design docs (one of them shipping to
// every consumer, its own filename naming a private product), a consumer's domain baked into a
// wire-format constant emitted into every generated manifest, real internal hostnames in test
// fixtures, and named private products scattered through prose that had been copy-edited past
// the rule dozens of times. This repository is public. Zero-tolerance.
//
//   node tools/naming-gate.mjs check          # fail on any real name in content or filenames
//   node tools/naming-gate.mjs hash <term>    # compute the digest for a new naming-terms entry
//
// The forbidden terms are stored as SHA-256 digests, never literals. A guard that spelled the
// names would itself be the worst violation in the tree: one public file enumerating every
// private product and the consumer org. AGENTS.md line 35 ("holds for public and private files
// alike") binds this gate too.
//
// Be honest about what that buys: digests of short, guessable words are dictionary-attackable.
// This is not secrecy. It raises the bar from "read one file" to "mount an offline attack",
// which is proportionate, because the goal is to avoid STATING the relationship, not to defend
// a secret. Do not describe naming-terms.json as if it were confidential.
//
// The module root is the parent of tools/, so this works identically in canonical's `module/`
// and a consumer's `modules/<name>/`.

import { createHash } from "node:crypto";
import { readFileSync, readdirSync, statSync, lstatSync, existsSync } from "node:fs";
import { join, relative, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const MODULE_ROOT = join(dirname(SCRIPT_PATH), "..");

// Mirrors base-integrity's prune set: build output, dependencies, VCS.
const PRUNE_DIRS = new Set([
  "node_modules", ".next", ".turbo", "dist", "build", "coverage",
  ".git", "vendor", "__pycache__", ".codefly", ".cache", ".nix-cache", "test-results", "playwright-report",
]);

// Generated output is deliberately NOT skipped — a checked-in generated Go file carried a
// product name that only a scan of generated code would have caught. Only content it cannot
// read usefully is
// skipped: binaries, and machine-generated files with no prose (a lockfile's dependency names
// and the base manifest's digests produce noise, never a real violation).
// Matched on the suffix, not the whole path: `rel` is relative to the scan root, which is the
// repository root in canonical (`module/tools/...`) and the module root in a consumer copy
// (`tools/...`).
const SKIP_FILE = (rel) =>
  /(?:^|\/)tools\/base-manifest\.json$/.test(rel) ||
  /(?:^|\/)tools\/naming-terms\.json$/.test(rel) || // digests only, by construction
  /(?:^|\/)package-lock\.json$/.test(rel) ||
  /\.(?:png|jpe?g|gif|webp|avif|ico|icns|pdf|zip|gz|tgz|bz2|xz|woff2?|ttf|otf|eot|mp4|webm|wasm|so|dylib|dll|exe|bin|node)$/i.test(rel) ||
  rel.endsWith(".tsbuildinfo") ||
  rel.endsWith(".DS_Store");

const MAX_BYTES = 512 * 1024;

// A slug is an identifier-ish run: a hyphenated repository name, a dotted hostname, an email
// address, an UPPER_SNAKE constant, a camelCase identifier. Splitting it on separators yields
// the parts a term can match; keeping the part count lets `compound` distinguish a name embedded
// in an identifier from the same letters used as an ordinary English word.
const SLUG_RE = /[A-Za-z0-9][A-Za-z0-9._@-]*[A-Za-z0-9]|[A-Za-z0-9]+/g;
const WORD_RE = /[A-Za-z]+/g;

const digest = (value) => createHash("sha256").update(value).digest("hex");

// Modes, and why each exists:
//   slug     — the whole normalized slug. For names whose individual words are far too generic
//              to forbid on their own, where only the hyphenated whole is distinctive.
//   word     — any constituent word, case-insensitively. The default, for distinctive names.
//   proper   — a constituent word written as a proper noun, /^[A-Z][a-z]+$/. For names that
//              collide with ordinary English, so that the product spelled as a proper noun
//              fails while the ordinary word, and an UPPER_SNAKE constant, do not.
//   compound — a constituent word, but only inside a multi-part slug. The other half of the
//              English-collision problem: a real name embedded in an identifier is signal, the
//              bare word is not. Pair it with `proper`; never use it for a name whose letters
//              appear in a common constant, or every such constant fails the gate.
//   phrase   — a lowercased 2- or 3-word n-gram. For human and company names written with
//              spaces, where no single word is distinctive enough to forbid.
const MODES = new Set(["slug", "word", "proper", "compound", "phrase"]);

function loadTerms(root = MODULE_ROOT) {
  const path = join(root, "tools", "naming-terms.json");
  if (!existsSync(path)) return null;
  let parsed;
  try {
    parsed = JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return null;
  }
  const index = { slug: new Set(), word: new Set(), proper: new Set(), compound: new Set(), phrase: new Set() };
  for (const entry of Array.isArray(parsed?.terms) ? parsed.terms : []) {
    // A malformed entry is dropped rather than guessed at, so a typo weakens the gate
    // visibly (the tree stops failing on a name) instead of silently matching nothing.
    if (typeof entry?.h !== "string" || !/^[0-9a-f]{64}$/.test(entry.h)) continue;
    const modes = Array.isArray(entry.modes) ? entry.modes : [entry.mode ?? "word"];
    for (const mode of modes) if (MODES.has(mode)) index[mode].add(entry.h);
  }
  return index;
}

function loadAllowlist(root = MODULE_ROOT) {
  const path = join(root, "tools", "naming-allowlist.json");
  if (!existsSync(path)) return new Set();
  let parsed;
  try {
    parsed = JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return new Set();
  }
  const allowed = new Set();
  for (const entry of Array.isArray(parsed?.paths) ? parsed.paths : []) {
    // Same contract as authz-coverage-allowlist.json: an exemption without a reason and a
    // ticket is not a reviewable decision, so it does not take effect.
    if (typeof entry?.path !== "string") continue;
    if (typeof entry?.reason !== "string" || !entry.reason.trim()) continue;
    if (typeof entry?.ticket !== "string" || !entry.ticket.trim()) continue;
    allowed.add(entry.path);
  }
  return allowed;
}

// Every (mode, digest) a single slug occurrence can satisfy.
function slugHits(raw, terms) {
  const hits = [];
  const lower = raw.toLowerCase();
  if (terms.slug.has(digest(lower))) hits.push(raw);

  const parts = raw.split(/[._@-]+/).filter(Boolean);
  const compound = parts.length > 1;
  for (const part of parts) {
    const h = digest(part.toLowerCase());
    if (terms.word.has(h)) hits.push(part);
    else if (compound && terms.compound.has(h)) hits.push(part);
    else if (terms.proper.has(h) && /^[A-Z][a-z]+$/.test(part)) hits.push(part);
  }
  return hits;
}

function phraseHits(line, terms) {
  if (!terms.phrase.size) return [];
  const words = line.match(WORD_RE);
  if (!words) return [];
  const hits = [];
  for (let i = 0; i < words.length; i += 1) {
    for (let n = 2; n <= 3 && i + n <= words.length; n += 1) {
      const gram = words.slice(i, i + n);
      if (terms.phrase.has(digest(gram.join(" ").toLowerCase()))) hits.push(gram.join(" "));
    }
  }
  return hits;
}

function walk(dir, out, base) {
  for (const name of readdirSync(dir)) {
    if (PRUNE_DIRS.has(name)) continue;
    const abs = join(dir, name);
    // lstat, not stat: `modules/<name>` is a symlink to `module/` (the workspace-composed view
    // Codefly expects). Following it walks the whole module tree a second time and reports every
    // violation twice, under a path that does not exist on disk.
    const st = lstatSync(abs);
    if (st.isSymbolicLink()) continue;
    if (st.isDirectory()) walk(abs, out, base);
    else if (st.isFile()) out.push(relative(base, abs));
  }
  return out;
}

// Widen to the repository root when running against canonical, so root *.md and .github/ are
// covered; stay inside the module when running in a consumer copy. Same test base-integrity
// uses for its public-claim scan.
export function canonicalScanRoot(moduleRoot = MODULE_ROOT) {
  const repositoryRoot = dirname(moduleRoot);
  if (
    resolve(join(repositoryRoot, "module")) === resolve(moduleRoot) &&
    existsSync(join(repositoryRoot, "workspace.codefly.yaml"))
  ) {
    return repositoryRoot;
  }
  return moduleRoot;
}

export function namingErrors(moduleRoot = MODULE_ROOT, scanRoot = canonicalScanRoot(moduleRoot)) {
  const terms = loadTerms(moduleRoot);
  if (!terms) return ["tools/naming-terms.json is missing or not valid JSON"];

  const allowed = loadAllowlist(moduleRoot);
  const errors = [];

  for (const rel of walk(scanRoot, [], scanRoot).sort()) {
    if (SKIP_FILE(rel) || allowed.has(rel)) continue;

    // A content-only scan misses a file that names a product in its own filename — which is
    // how the worst offender in the tree shipped to every consumer.
    for (const hit of new Set(rel.split("/").flatMap((seg) => slugHits(seg, terms)))) {
      errors.push(`${rel}: forbidden name in path (${hit})`);
    }

    const abs = join(scanRoot, rel);
    if (statSync(abs).size > MAX_BYTES) continue;
    let source;
    try {
      source = readFileSync(abs, "utf8");
    } catch {
      continue;
    }
    if (source.includes("\0")) continue; // binary that slipped the extension list

    source.split("\n").forEach((line, i) => {
      const hits = new Set([
        ...(line.match(SLUG_RE) ?? []).flatMap((slug) => slugHits(slug, terms)),
        ...phraseHits(line, terms),
      ]);
      for (const hit of hits) errors.push(`${rel}:${i + 1}: forbidden name (${hit})`);
    });
  }
  return errors.sort();
}

function check() {
  const errors = namingErrors();
  if (errors.length) {
    console.error("naming-gate: real customer, product, or consumer names in a repository that forbids them:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} forbidden name(s). Use generic placeholders — "a consuming ` +
        `solution", "the downstream product", "Acme", "Jane Doe", "user@example.com". See ` +
        `AGENTS.md §"Naming and confidentiality". A genuine exception (a copyright holder, a ` +
        `CODEOWNERS handle) goes in tools/naming-allowlist.json with a reason and a ticket.`,
    );
    process.exit(1);
  }
  console.log("✓ no real customer, product, or consumer names in tracked content or filenames.");
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  const cmd = process.argv[2];
  if (cmd === "check") check();
  else if (cmd === "hash") {
    const term = process.argv[3];
    if (!term) {
      console.error("usage: naming-gate.mjs hash <term>");
      process.exit(2);
    }
    console.log(digest(term.toLowerCase()));
  } else {
    console.error("usage: naming-gate.mjs <check|hash>");
    process.exit(2);
  }
}
