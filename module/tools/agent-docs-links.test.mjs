// agent-docs-links — every reference into an AGENTS.md must resolve.
//
// AGENTS.md is a multi-file structure: a root file, nested files per tree, and docs
// that link into their sections. Splitting the root moved sections out of it, and
// three in-tree references kept pointing at anchors that no longer existed —
// including `module/deployment/README.md`, whose steps 3 and 4 pointed at the
// procedures for regenerating manifests and refreshing the base manifest, at the one
// step that README says fails CI when skipped. Nothing caught it: no gate resolves a
// link, so CI was green with the pointers dead.
//
// This holds links *into* agent-context files, in both directions: a reference to a
// section that does not exist, and a reference that escapes the module tree (which
// resolves in canonical and breaks in every consumer, because the module root is
// `modules/<name>/` there).

import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";

const REPO = execFileSync("git", ["rev-parse", "--show-toplevel"], {
  encoding: "utf8",
}).trim();
const MODULE_ROOT = join(REPO, "module");

/** Every tracked markdown file, repository-relative. */
function trackedMarkdown() {
  return execFileSync("git", ["ls-files", "-z", "*.md"], {
    cwd: REPO,
    encoding: "utf8",
    maxBuffer: 32 * 1024 * 1024,
  })
    .split("\0")
    .filter(Boolean);
}

/** GitHub's heading -> anchor slug. */
function slug(heading) {
  return heading
    .trim()
    .toLowerCase()
    .replaceAll("`", "")
    .replace(/[^\p{L}\p{N}\s-]/gu, "")
    .trim()
    .replace(/\s+/g, "-");
}

function anchorsOf(file) {
  const found = new Set();
  for (const line of readFileSync(file, "utf8").split("\n")) {
    const heading = /^#{1,6}\s+(.*)$/.exec(line);
    if (heading) found.add(slug(heading[1]));
  }
  return found;
}

/** Markdown links whose target path names an AGENTS.md (with or without an anchor). */
function agentsLinks(file) {
  const body = readFileSync(join(REPO, file), "utf8");
  const links = [];
  for (const [, path, anchor] of body.matchAll(/\]\(([^)\s#]*)(#[^)\s]*)?\)/g)) {
    if (!/AGENTS\.md$/.test(path)) continue;
    if (/^[a-z][a-z0-9+.-]*:/i.test(path)) continue; // external URL
    links.push({ path, anchor: anchor ? anchor.slice(1) : null });
  }
  return links;
}

test("every link into an AGENTS.md resolves, section included", () => {
  const dead = [];
  for (const file of trackedMarkdown()) {
    for (const { path, anchor } of agentsLinks(file)) {
      const target = resolve(dirname(join(REPO, file)), path);
      if (!existsSync(target)) {
        dead.push(`${file} -> ${path} (no such file)`);
        continue;
      }
      if (anchor && !anchorsOf(target).has(anchor)) {
        dead.push(`${file} -> ${path}#${anchor} (no such section)`);
      }
    }
  }
  assert.deepEqual(
    dead,
    [],
    `a reference into an AGENTS.md does not resolve. Moving a section out of a file ` +
      `orphans every pointer at it:\n  ${dead.join("\n  ")}`,
  );
});

test("a file in the module tree never links to an AGENTS.md outside it", () => {
  // The module tree is copied into a consumer at `modules/<name>/`, so a link that
  // climbs above the module root resolves only in this repository.
  const escaping = [];
  for (const file of trackedMarkdown()) {
    const absolute = join(REPO, file);
    if (relative(MODULE_ROOT, absolute).startsWith("..")) continue;
    for (const { path } of agentsLinks(file)) {
      const target = resolve(dirname(absolute), path);
      if (relative(MODULE_ROOT, target).startsWith("..")) {
        escaping.push(`${file} -> ${path}`);
      }
    }
  }
  assert.deepEqual(
    escaping,
    [],
    `a shipped file links to an AGENTS.md above the module root, which does not exist ` +
      `in a consumer:\n  ${escaping.join("\n  ")}`,
  );
});
