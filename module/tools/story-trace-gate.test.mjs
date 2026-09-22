import assert from "node:assert/strict";
import test from "node:test";

import {
  storiesInSource,
  storiesOnPage,
  storyTestErrors,
  storyTraceErrors,
} from "./story-trace-gate.mjs";

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
  assert.deepEqual(
    [...storiesInSource(go)],
    [["HOST-ID-001", { named: "TestStory_HOST_ID_001", skipped: false }]],
  );
  const ts = 'describe("HOST-JOB-001 · Work is never lost", () => {});';
  assert.deepEqual(
    [...storiesInSource(ts)],
    [["HOST-JOB-001", { named: 'describe("HOST-JOB-001")', skipped: false }]],
  );
});

test("a test that skips itself is recorded as unproven", () => {
  const go = `
func TestStory_HOST_ID_001(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a database")
	}
	require.True(t, true)
}
`;
  assert.deepEqual(
    [...storiesInSource(go)],
    [["HOST-ID-001", { named: "TestStory_HOST_ID_001", skipped: true }]],
  );
  assert.deepEqual(
    [...storiesInSource('describe.skip("HOST-JOB-001 · pending", () => {});')],
    [["HOST-JOB-001", { named: 'describe("HOST-JOB-001")', skipped: true }]],
  );
});

test("a skip in a neighbouring function does not taint the story test", () => {
  const go = `
func TestStory_HOST_ID_001(t *testing.T) {
	require.True(t, true)
}

func TestSomethingElse(t *testing.T) {
	t.Skip("unrelated")
}
`;
  assert.equal(storiesInSource(go).get("HOST-ID-001").skipped, false);
});

test("a skipped story fails the trace", () => {
  const tests = new Map([
    ["HOST-ID-001", { named: "identity_test.go:TestStory_HOST_ID_001", skipped: false }],
    ["HOST-JOB-001", { named: "jobs_test.go:TestStory_HOST_JOB_001", skipped: true }],
  ]);
  const errors = storyTraceErrors(storiesOnPage(page), tests);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /skips itself, so the story is unproven/);
});

test("an ordinary test name is not read as a story", () => {
  assert.deepEqual([...storiesInSource("func TestJobsAreRedelivered(t *testing.T) {}")], []);
});

test("a fully traced page passes", () => {
  const tests = new Map([
    ["HOST-ID-001", { named: "identity_test.go:TestStory_HOST_ID_001", skipped: false }],
    ["HOST-JOB-001", { named: "jobs_test.go:TestStory_HOST_JOB_001", skipped: false }],
  ]);
  assert.deepEqual(storyTraceErrors(storiesOnPage(page), tests), []);
});

test("a story with no test fails", () => {
  const tests = new Map([
    ["HOST-ID-001", { named: "identity_test.go:TestStory_HOST_ID_001", skipped: false }],
  ]);
  const errors = storyTraceErrors(storiesOnPage(page), tests);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /HOST-JOB-001 is on the page but no test proves it/);
  assert.match(errors[0], /TestStory_HOST_JOB_001/);
});

test("a test naming a story the page dropped fails", () => {
  const tests = new Map([
    ["HOST-ID-001", { named: "identity_test.go:TestStory_HOST_ID_001", skipped: false }],
    ["HOST-JOB-001", { named: "jobs_test.go:TestStory_HOST_JOB_001", skipped: false }],
    ["HOST-AUD-001", { named: "audit_test.go:TestStory_HOST_AUD_001", skipped: false }],
  ]);
  const errors = storyTraceErrors(storiesOnPage(page), tests);
  assert.equal(errors.length, 1);
  assert.match(errors[0], /audit_test\.go:TestStory_HOST_AUD_001 names HOST-AUD-001/);
});

test("the page-free half rejects a story test that skips itself", () => {
  const errors = storyTestErrors(
    new Map([
      ["HOST-ID-001", { named: "identity_test.go:TestStory_HOST_ID_001", skipped: false }],
      ["HOST-JOB-001", { named: "jobs_test.go:TestStory_HOST_JOB_001", skipped: true }],
    ]),
  );
  assert.equal(errors.length, 1);
  assert.match(errors[0], /HOST-JOB-001 but skips itself/);
});

test("the page-free half passes when every story test runs", () => {
  const errors = storyTestErrors(
    new Map([["HOST-ID-001", { named: "identity_test.go:TestStory_HOST_ID_001", skipped: false }]]),
  );
  assert.deepEqual(errors, []);
});

test("a tree with no story tests fails rather than passing vacuously", () => {
  const errors = storyTestErrors(new Map());
  assert.equal(errors.length, 1);
  assert.match(errors[0], /refusing to pass vacuously/);
});
