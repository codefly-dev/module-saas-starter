import assert from "node:assert/strict";
import test from "node:test";

import { storiesInSource, storiesOnPage, storyTraceErrors } from "./story-trace-gate.mjs";

const page = `# saas-starter — the host

## User stories

### HOST-ID-001 · Every request carries who and for whom
As a module, I want every request to arrive with a verifiable identity.

### HOST-JOB-001 · Work is never lost
As a module, I want a job I claim to be redelivered if I crash.
`;

test("stories are the page's headings", () => {
  assert.deepEqual([...storiesOnPage(page)], ["HOST-ID-001", "HOST-JOB-001"]);
});

test("a story id mentioned in prose is not itself a story", () => {
  const withReference = `${page}\nSupersedes HOST-AUD-001, which moved to the audit page.\n`;
  assert.deepEqual([...storiesOnPage(withReference)], ["HOST-ID-001", "HOST-JOB-001"]);
});

test("Go test functions and TypeScript suites both name their story", () => {
  const go = "func TestStory_HOST_ID_001(t *testing.T) {}";
  assert.deepEqual([...storiesInSource(go)], [["HOST-ID-001", "TestStory_HOST_ID_001"]]);
  const ts = 'describe("HOST-JOB-001 · Work is never lost", () => {});';
  assert.deepEqual([...storiesInSource(ts)], [["HOST-JOB-001", 'describe("HOST-JOB-001")']]);
});

test("an ordinary test name is not read as a story", () => {
  assert.deepEqual([...storiesInSource("func TestJobsAreRedelivered(t *testing.T) {}")], []);
});

test("a fully traced page passes", () => {
  const tests = new Map([
    ["HOST-ID-001", "identity_test.go:TestStory_HOST_ID_001"],
    ["HOST-JOB-001", "jobs_test.go:TestStory_HOST_JOB_001"],
  ]);
  assert.deepEqual(storyTraceErrors(storiesOnPage(page), tests), []);
});

test("a story with no test fails", () => {
  const tests = new Map([["HOST-ID-001", "identity_test.go:TestStory_HOST_ID_001"]]);
  const errors = storyTraceErrors(storiesOnPage(page), tests);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /HOST-JOB-001 is on the page but no test proves it/);
  assert.match(errors[0], /TestStory_HOST_JOB_001/);
});

test("a test naming a story the page dropped fails", () => {
  const tests = new Map([
    ["HOST-ID-001", "identity_test.go:TestStory_HOST_ID_001"],
    ["HOST-JOB-001", "jobs_test.go:TestStory_HOST_JOB_001"],
    ["HOST-AUD-001", "audit_test.go:TestStory_HOST_AUD_001"],
  ]);
  const errors = storyTraceErrors(storiesOnPage(page), tests);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /audit_test\.go:TestStory_HOST_AUD_001 names HOST-AUD-001/);
});
