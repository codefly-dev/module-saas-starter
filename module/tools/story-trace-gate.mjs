#!/usr/bin/env node
// story-trace-gate — keeps the module's user stories and its acceptance tests
// from drifting apart.
//
// The functional contract for this module lives outside the repository, as a
// page of user stories with stable `HOST-*` ids. A story is a claim about
// behaviour, so every story needs a test here that proves it, and a test that
// claims a story id has to be proving a story that still exists. Neither
// direction is visible from inside one repository alone, which is why they
// drift silently: a story is reworded and its test keeps passing against the
// old behaviour, or a story is dropped and its test lingers as the only
// remaining description of a requirement nobody holds any more.
//
// The trace is symmetric and both directions are errors:
//
//   story on the page with no test here  -> an unproven claim
//   test naming an absent story          -> a claim nobody owns
//
//   node tools/story-trace-gate.mjs check --page <handbook-page.md>
//
// That comparison needs the page, so it belongs wherever the page and this
// tree sit together — a composed workspace has both, and this tool ships with
// the module for exactly that. Where only the tree is available, `tests` runs
// the half that needs no page: that every story test here is present and
// actually runs. It deliberately does not report on the page comparison, since
// a check that quietly stands in for one it never made is worse than none.
//
//   node tools/story-trace-gate.mjs tests
//
// The module root is the parent of tools/, so this works identically in
// canonical's `module/` and a consumer's `modules/<name>/`.

import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const MODULE_ROOT = join(dirname(SCRIPT_PATH), "..");

// A story id as it appears on the page and, with dashes as underscores, in a
// test name.
const STORY_ID = /\bHOST-[A-Z]+-\d{3}\b/g;

// Stories are the page's own headings. A story id quoted in prose elsewhere on
// the page (a cross-reference, a changelog line) is a mention, not a claim this
// repository has to prove.
const STORY_HEADING = /^#{2,4}\s+.*$/gm;

// Go names the story in the test function; TypeScript names it in the suite.
const GO_STORY_TEST = /func\s+(TestStory_HOST_[A-Z]+_\d{3})\s*\([^)]*\)\s*\{/g;
const TS_STORY_TEST = /describe(?:\.skip)?\(\s*["'`](HOST-[A-Z]+-\d{3})\b/g;

// A test that declares a story and then skips itself proves nothing, so the
// declaration alone is not the signal — the body has to actually run.
const GO_SKIP = /\bt\.Skip(?:f|Now)?\s*\(/;
const TS_SKIPPED_SUITE = /describe\.skip\(\s*["'`]$/;

// tools/ holds the gates themselves. Their fixtures contain literal story
// declarations as test data, and counting a fixture as proof would let this
// gate satisfy itself; acceptance tests live under services/.
const SKIP_DIRECTORIES = new Set([
  "node_modules",
  ".next",
  ".git",
  "gen",
  "generated",
  "vendor",
  "tools",
]);
const TEST_FILE = /(_test\.go|\.(?:test|spec)\.[cm]?[jt]sx?)$/;

const testNameToStory = (name) => name.replace(/^TestStory_/, "").replaceAll("_", "-");

export function storiesOnPage(markdown) {
  const stories = new Set();
  for (const heading of markdown.match(STORY_HEADING) ?? []) {
    for (const id of heading.match(STORY_ID) ?? []) {
      stories.add(id);
    }
  }
  return stories;
}

// The body of the Go function that starts at `open` (the index of its opening
// brace), found by brace matching.
function functionBody(source, open) {
  let depth = 0;
  for (let i = open; i < source.length; i++) {
    if (source[i] === "{") {
      depth++;
      continue;
    }
    if (source[i] === "}") {
      depth--;
      if (depth === 0) {
        return source.slice(open, i + 1);
      }
    }
  }
  return source.slice(open);
}

export function storiesInSource(source) {
  const stories = new Map();
  for (const match of source.matchAll(GO_STORY_TEST)) {
    const name = match[1];
    const body = functionBody(source, source.indexOf("{", match.index + match[0].length - 1));
    stories.set(testNameToStory(name), { named: name, skipped: GO_SKIP.test(body) });
  }
  for (const match of source.matchAll(TS_STORY_TEST)) {
    const id = match[1];
    stories.set(id, {
      named: `describe("${id}")`,
      skipped: TS_SKIPPED_SUITE.test(source.slice(0, match.index + match[0].length - id.length)),
    });
  }
  return stories;
}

export function storyTraceErrors(onPage, inTests) {
  const errors = [];
  for (const story of [...onPage].sort()) {
    const test = inTests.get(story);
    if (!test) {
      errors.push(
        `${story} is on the page but no test proves it; add func TestStory_${story.replaceAll("-", "_")}` +
          ` or describe("${story} …")`,
      );
      continue;
    }
    if (test.skipped) {
      errors.push(`${test.named} names ${story} but skips itself, so the story is unproven`);
    }
  }
  for (const [story, test] of [...inTests.entries()].sort(([a], [b]) => a.localeCompare(b))) {
    if (!onPage.has(story)) {
      errors.push(`${test.named} names ${story}, which is not a story on the page`);
    }
  }
  return errors;
}

function* testFiles(directory) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (!SKIP_DIRECTORIES.has(entry.name)) {
        yield* testFiles(join(directory, entry.name));
      }
      continue;
    }
    if (TEST_FILE.test(entry.name)) {
      yield join(directory, entry.name);
    }
  }
}

function collectTests() {
  const stories = new Map();
  for (const file of testFiles(MODULE_ROOT)) {
    for (const [story, test] of storiesInSource(readFileSync(file, "utf8"))) {
      stories.set(story, { ...test, named: `${relative(MODULE_ROOT, file)}:${test.named}` });
    }
  }
  return stories;
}

// Errors that need no page: a story test that skips itself proves nothing, and
// a tree with no story tests at all has lost them rather than earned silence.
export function storyTestErrors(inTests) {
  if (inTests.size === 0) {
    return ["no TestStory_HOST_* acceptance tests found; refusing to pass vacuously"];
  }
  return [...inTests.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .filter(([, test]) => test.skipped)
    .map(([story, test]) => `${test.named} names ${story} but skips itself, so the story is unproven`);
}

function tests() {
  const collected = collectTests();
  const errors = storyTestErrors(collected);
  if (errors.length > 0) {
    for (const error of errors) {
      console.error(`error: ${error}`);
    }
    process.exit(1);
  }
  console.log(
    `story tests OK: ${collected.size} stories have a test that runs ` +
      `(the comparison against the functional page runs where that page is available)`,
  );
}

function check(pagePath) {
  const onPage = storiesOnPage(readFileSync(pagePath, "utf8"));
  if (onPage.size === 0) {
    console.error(`error: ${pagePath} carries no HOST-* story headings; refusing to pass vacuously`);
    process.exit(1);
  }
  const errors = storyTraceErrors(onPage, collectTests());
  if (errors.length > 0) {
    for (const error of errors) {
      console.error(`error: ${error}`);
    }
    process.exit(1);
  }
  console.log(`story trace OK: ${onPage.size} stories, each proven by a named test`);
}

if (process.argv[1] === SCRIPT_PATH) {
  const [command, flag, pagePath] = process.argv.slice(2);
  if (command === "tests") {
    tests();
  } else if (command === "check" && flag === "--page" && pagePath) {
    check(pagePath);
  } else {
    console.error(
      "usage: node tools/story-trace-gate.mjs check --page <handbook-page.md>\n" +
        "       node tools/story-trace-gate.mjs tests",
    );
    process.exit(2);
  }
}
