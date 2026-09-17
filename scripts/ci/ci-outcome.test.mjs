import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const script = resolve(import.meta.dirname, 'ci-outcome.mjs');

function render(report, { summaryFile } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'ci-outcome-'));
  try {
    const path = join(dir, 'report.json');
    writeFileSync(path, JSON.stringify(report));
    const env = { ...process.env };
    delete env.GITHUB_STEP_SUMMARY;
    if (summaryFile) env.GITHUB_STEP_SUMMARY = join(dir, 'summary.md');
    const result = spawnSync(process.execPath, [script, path], { encoding: 'utf8', env });
    return {
      ...result,
      summary: summaryFile ? readFileSync(join(dir, 'summary.md'), 'utf8') : undefined,
    };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const executedTask = {
  resource: 'saas-starter/accounts', status: 'passed', duration_ms: 352000,
  cache: { status: 'miss', stored: true },
};
const reusedTask = {
  resource: 'saas-starter/frontend', status: 'passed',
  cache: {
    status: 'hit', stored: false,
    reuse: { reference: 'main', run: '42/1', revision: 'abcdef1234567890', recorded_at: new Date().toISOString() },
  },
};

// The split is the whole point: a reused task did not run, and a reader who
// cannot separate it from a verified one cannot tell what a green check covered.
test('a reused task is reported as not run, with the evidence it stood on', () => {
  const { stdout, status } = render({ phase: 'test', tasks: [executedTask, reusedTask] });
  assert.equal(status, 0);
  assert.match(stdout, /\*\*1 executed\*\*, \*\*1 reused\*\*/);
  assert.match(stdout, /verified now[\s\S]*saas-starter\/accounts/);
  assert.match(stdout, /NOT run — reused prior evidence[\s\S]*saas-starter\/frontend/);
  assert.match(stdout, /main @ 42\/1 \(abcdef12\)/, 'the reused row must name the run that signed it');
  assert.match(stdout, /0m ago/, 'a reader judges reuse by how stale the evidence is');
  assert.doesNotMatch(stdout, /saas-starter\/frontend \| passed/, 'a reused task must not read as verified now');
});

// The failure this whole report exists to expose: passing, green, and storing
// nothing, so every later run repeats the work.
test('a passing task that published nothing is called out with its reason', () => {
  const withheld = {
    resource: 'saas-starter/store', status: 'passed',
    cache: { status: 'miss', stored: false, reason: 'the resolved agent binary is unbound' },
  };
  const { stdout } = render({ phase: 'test', tasks: [withheld] });
  assert.match(stdout, /Results were not published/);
  assert.match(stdout, /saas-starter\/store.*the resolved agent binary is unbound/);
});

test('a stored task is not mistaken for a withheld one', () => {
  const { stdout } = render({ phase: 'test', tasks: [executedTask] });
  assert.doesNotMatch(stdout, /Results were not published/);
});

test('services outside the plan are reported as untested, with the reason', () => {
  const { stdout } = render({
    phase: 'test',
    tasks: [executedTask],
    not_selected: [{ service: 'saas-starter/marketing', reason: 'nothing it depends on changed' }],
  });
  assert.match(stdout, /NOT tested — outside this plan[\s\S]*saas-starter\/marketing/);
  assert.match(stdout, /nothing it depends on changed/);
});

// A phase that selected nothing writes no report. That is a normal outcome of
// affected-service scoping, not a failure to announce.
test('a missing report explains itself and does not fail the phase', () => {
  const result = spawnSync(process.execPath, [script, '/nonexistent/report.json'], { encoding: 'utf8' });
  assert.equal(result.status, 0);
  assert.match(result.stdout, /nothing to report/);
});

test('the outcome is written to the GitHub step summary when one is provided', () => {
  const { summary } = render({ phase: 'test', tasks: [reusedTask] }, { summaryFile: true });
  assert.match(summary, /NOT run — reused prior evidence/);
});
