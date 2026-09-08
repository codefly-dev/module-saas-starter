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
const GO_STORY_TEST = /func\s+(TestStory_HOST_[A-Z]+_\d{3})\s*\(/g;
const TS_STORY_TEST = /describe\(\s*["'`](HOST-[A-Z]+-\d{3})\b/g;

const SKIP_DIRECTORIES = new Set(["node_modules", ".next", ".git", "gen", "generated", "vendor"]);
const TEST_FILE = /(_test\.go|\.test\.[jt]sx?|\.spec\.[jt]sx?)$/;

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

export function storiesInSource(source) {
  const stories = new Map();
  for (const [, name] of source.matchAll(GO_STORY_TEST)) {
    stories.set(testNameToStory(name), name);
  }
  for (const [, id] of source.matchAll(TS_STORY_TEST)) {
    stories.set(id, `describe("${id}")`);
  }
  return stories;
}

export function storyTraceErrors(onPage, inTests) {
  const errors = [];
  for (const story of [...onPage].sort()) {
    if (!inTests.has(story)) {
      errors.push(
        `${story} is on the page but no test proves it; add func TestStory_${story.replaceAll("-", "_")}`,
      );
    }
  }
  for (const [story, named] of [...inTests.entries()].sort(([a], [b]) => a.localeCompare(b))) {
    if (!onPage.has(story)) {
      errors.push(`${named} names ${story}, which is not a story on the page`);
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
    for (const [story, named] of storiesInSource(readFileSync(file, "utf8"))) {
      stories.set(story, `${relative(MODULE_ROOT, file)}:${named}`);
    }
  }
  return stories;
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
  if (command !== "check" || flag !== "--page" || !pagePath) {
    console.error("usage: node tools/story-trace-gate.mjs check --page <handbook-page.md>");
    process.exit(2);
  }
  check(pagePath);
}
