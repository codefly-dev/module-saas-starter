import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFile, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { promisify } from 'node:util';
import test from 'node:test';
import { parseWorkflowYaml } from './workflow-yaml.mjs';

const root = join(import.meta.dirname, '../..');
const workflow = parseWorkflowYaml(readFileSync(join(root, '.github/workflows/ci.yml'), 'utf8'));

test('the CLI installer retries resets, verifies downloads, and fails after exhausting retries', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'ci-cli-'));
  let attempts = 0;
  let outcome = 'recover';
  const server = createServer((request, response) => {
    attempts++;
    if (outcome === 'fail' || (outcome === 'recover' && attempts === 1)) {
      request.socket.resetAndDestroy();
      return;
    }
    response.end(readFileSync(join(dir, 'fixture.tar.gz')));
  });
  try {
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    writeFileSync(join(dir, 'codefly'), '#!/bin/sh\necho fixture\n');
    const archive = join(dir, 'fixture.tar.gz');
    assert.equal(spawnSync('tar', ['-czf', archive, '-C', dir, 'codefly']).status, 0);
    const digest = createHash('sha256').update(readFileSync(archive)).digest('hex');
    const installer = readFileSync(join(root, 'scripts/ci/install-codefly.sh'), 'utf8');
    writeFileSync(join(dir, 'installer.sh'), installer
      .replace(/checksum=[a-f0-9]{64}/g, `checksum=${digest}`)
      .replace('https://github.com/codefly-dev/cli/releases/download/v${version}/${archive}', `http://127.0.0.1:${server.address().port}/archive`));
    writeFileSync(join(dir, 'uname'), '#!/bin/sh\nif [ "$1" = -s ]; then echo Linux; else echo x86_64; fi\n', { mode: 0o755 });
    const pathFile = join(dir, 'github-path');
    const env = { ...process.env, PATH: `${dir}:${process.env.PATH}`, RUNNER_TEMP: dir, GITHUB_PATH: pathFile, NO_PROXY: '127.0.0.1', no_proxy: '127.0.0.1' };
    const runInstaller = () => promisify(execFile)('bash', [join(dir, 'installer.sh')], { env, encoding: 'utf8', timeout: 30000 });
    await runInstaller();
    assert.equal(attempts, 2);
    assert.equal(readFileSync(join(dir, 'codefly-bin/codefly'), 'utf8'), '#!/bin/sh\necho fixture\n');
    assert.equal(readFileSync(pathFile, 'utf8'), `${dir}/codefly-bin\n`);
    writeFileSync(pathFile, '');
    outcome = 'corrupt';
    attempts = 0;
    writeFileSync(archive, 'corrupt download');
    await assert.rejects(runInstaller(), { code: 1 });
    assert.equal(attempts, 1);
    assert.equal(readFileSync(pathFile, 'utf8'), '');
    outcome = 'fail';
    attempts = 0;
    await assert.rejects(runInstaller(), { code: 56 });
    assert.equal(attempts, 4);
    assert.equal(readFileSync(pathFile, 'utf8'), '');
  } finally {
    await new Promise(resolve => server.close(resolve));
    rmSync(dir, { recursive: true, force: true });
  }
});

const DEFAULT_CODEFLY_VERSION = '0.1.168';

// The planner's selection and every phase's execution must come from one
// CLI: the plan-only 0.1.151 override (#743) existed while the published
// agent fleet predated that release's runtime, and a split lets the planner
// select on rules a phase does not enforce.
function assertEveryJobUsesTheDefaultCodefly(candidateWorkflow) {
  const installs = Object.entries(candidateWorkflow.jobs).flatMap(([jobName, job]) =>
    (job.steps ?? [])
      .filter(step => step.run === 'bash scripts/ci/install-codefly.sh')
      .map(step => ({
        jobName,
        version: step.env?.CODEFLY_VERSION
          ?? job.env?.CODEFLY_VERSION
          ?? candidateWorkflow.env?.CODEFLY_VERSION
          ?? DEFAULT_CODEFLY_VERSION,
      })),
  );
  assert.ok(installs.length > 1);
  assert.deepEqual(installs.filter(install => install.version !== DEFAULT_CODEFLY_VERSION), []);
}

test('every Codefly job installs the one pinned CLI', () => {
  assertEveryJobUsesTheDefaultCodefly(workflow);
  const installer = readFileSync(join(root, 'scripts/ci/install-codefly.sh'), 'utf8');
  assert.match(installer, new RegExp(`CODEFLY_VERSION:-${DEFAULT_CODEFLY_VERSION.replaceAll('.', '\\.')}`));
});

// `--all` widens the service selection; it establishes no integrity inputs. A
// run given neither `--base` nor `--changed-file` plans with an integrity error
// and every phase behind a verify refuses, so forcing the full graph — which an
// image-contract change does — must still carry its change bounds.
test('every Codefly selection passes its change bounds, however wide the selection', () => {
  // Run the selection block itself rather than reading it: the bug this guards
  // put `--base` in the `else` of the `--all` branch, which any assertion over
  // the whole script's text still matches.
  const block = run => {
    const start = run.indexOf('selection_args=(--head');
    assert.notEqual(start, -1);
    const end = run.indexOf('\n\n', start);
    return run.slice(start, end === -1 ? undefined : end);
  };
  const args = (script, env) => {
    const shell = spawnSync('bash', ['-euo', 'pipefail', '-c', `${script}\nprintf '%s\\n' "\${selection_args[@]}"`],
      { encoding: 'utf8', env: { ...process.env, ...env } });
    assert.equal(shell.status, 0, shell.stderr);
    return shell.stdout.trim().split('\n');
  };
  const selections = Object.entries(workflow.jobs).flatMap(([jobName, job]) =>
    (job.steps ?? []).filter(step => (step.run ?? '').includes('selection_args=(--head'))
      .map(step => ({ jobName, run: step.run })));
  assert.deepEqual(selections.map(selection => selection.jobName).sort(),
    ['codefly-build', 'codefly-plan', 'codefly-quality-phases', 'codefly-supply-chain']);

  const head = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4';
  const base = 'da39a3ee5e6b4b0d3255bfef95601890afd80709';
  for (const { jobName, run } of selections.filter(selection => selection.jobName !== 'codefly-plan')) {
    for (const all of ['true', 'false']) {
      const resolved = args(block(run), { GITHUB_SHA: head, SELECTION_ALL: all, CODEFLY_BASE: base });
      assert.ok(resolved.includes('--base') && resolved.includes(base), `${jobName} (all=${all}) must pass its change bounds`);
      assert.equal(resolved.includes('--all'), all === 'true', `${jobName} (all=${all}) selection width`);
    }
    // A release tag reaches these jobs with whatever base the planner resolved;
    // an empty one must not become a `--base ''` the CLI cannot parse.
    const tagged = args(block(run), { GITHUB_SHA: head, SELECTION_ALL: 'true', CODEFLY_BASE: '' });
    assert.deepEqual(tagged, ['--head', head, '--all'], `${jobName} on a tag`);
  }

  // The fan-out jobs consume the planner's base verbatim rather than deciding
  // for themselves, so the fallback for a tag lives in the planner alone.
  const plan = workflow.jobs['codefly-plan'].steps.find(step => (step.run ?? '').includes('codefly ci plan'));
  assert.match(plan.run, /base="\$\(git rev-parse --verify --quiet "\$\{GITHUB_SHA\}\^"/);
  assert.match(plan.run, /integrity_error/);
});

test('the CLI scope guard includes inherited workflow and job environments', () => {
  const workflowOverride = structuredClone(workflow);
  workflowOverride.env = { ...workflowOverride.env, CODEFLY_VERSION: '0.1.151' };
  assert.throws(() => assertEveryJobUsesTheDefaultCodefly(workflowOverride), assert.AssertionError);

  const jobOverride = structuredClone(workflow);
  jobOverride.jobs['codefly-plan'].env = {
    ...jobOverride.jobs['codefly-plan'].env,
    CODEFLY_VERSION: '0.1.151',
  };
  assert.throws(() => assertEveryJobUsesTheDefaultCodefly(jobOverride), assert.AssertionError);
});

test('the CLI installer rejects versions outside its checksum allowlist', () => {
  const result = spawnSync('bash', [join(root, 'scripts/ci/install-codefly.sh')], {
    encoding: 'utf8',
    env: { ...process.env, CODEFLY_VERSION: '0.1.999' },
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Unsupported Codefly CI version: 0\.1\.999/);
});

test('the required Codefly quality check accepts only a successful phase matrix', () => {
  const gate = workflow.jobs['codefly-quality'];
  assert.equal(gate.name, 'Codefly quality');
  assert.ok(gate.needs.includes('codefly-quality-phases'));
  assert.ok(gate.if.includes('!cancelled()'));
  assert.equal(gate.steps[0].env.QUALITY_RESULT, '${{ needs.codefly-quality-phases.result }}');
  for (const outcome of ['success', 'failure', 'cancelled', 'skipped', '']) {
    const result = spawnSync('bash', ['-e', '-c', gate.steps[0].run], {
      env: { ...process.env, QUALITY_RESULT: outcome },
    });
    assert.equal(result.status === 0, outcome === 'success');
  }
});

const qualityPhaseScript = () =>
  workflow.jobs['codefly-quality-phases'].steps.find(step => step.name === 'Run quality phase').run;

// Runs a phase's command with `codefly` stubbed to echo its arguments, so the
// assertions below are about the invocation the runner would really make.
function runQualityPhase(phase, environment = {}) {
  const result = spawnSync('bash', ['-e', '-c', `codefly() { printf '%s\\n' "$*"; };\n${qualityPhaseScript()}`], {
    encoding: 'utf8',
    env: {
      ...process.env,
      CI_PHASE: phase,
      SELECTION_ALL: 'false',
      CODEFLY_BASE: 'base',
      GITHUB_SHA: 'head',
      RUNNER_TEMP: '/runner-temp',
      CODEFLY_CI_RESULT_KEY: '',
      ...environment,
    },
  });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

// Reconstructing the selection from `--changed-file module/services/<svc>/…`
// flattened the plan to a service list, discarding the direct/dependent
// classification and the reason each path was selected. Every phase now replays
// the plan the planner published, under bounds it supplies independently.
test('every quality phase replays the plan published for its own invocation', () => {
  const job = workflow.jobs['codefly-quality-phases'];
  assert.deepEqual(job.strategy.matrix.phase, ['sync-drift', 'lint', 'compile', 'test']);
  assert.equal(job.strategy['fail-fast'], 'false');
  for (const phase of job.strategy.matrix.phase) {
    const id = phase.replaceAll(',', '-');
    assert.deepEqual(runQualityPhase(phase).split('\n'), [
      `ci run --head head --base base --plan /runner-temp/ci-plan/${id}.json --phase ${phase} --output .codefly/ci/${id}`,
    ]);
  }
});

// A plan binds the exact invocation it was built for, so a phase whose plan the
// planner never published fails on a missing file, in a job that reads as if the
// phase itself broke.
test('the planner publishes a replay plan for every phase that replays one', () => {
  const planStep = workflow.jobs['codefly-plan'].steps.find(step => (step.run ?? '').includes('codefly ci plan'));
  const emission = planStep.run.slice(planStep.run.indexOf('plan_phases=('));
  assert.notEqual(planStep.run.indexOf('plan_phases=('), -1);
  const temporary = mkdtempSync(join(tmpdir(), 'ci-plan-'));
  try {
    const result = spawnSync('bash', ['-euo', 'pipefail', '-c',
      `codefly() { printf '%s\\n' "$*"; }\nselection_args=(--head head --base base)\n${emission}`], {
      encoding: 'utf8',
      env: { ...process.env, RUNNER_TEMP: temporary },
    });
    assert.equal(result.status, 0, result.stderr);

    // Read what each consumer actually asks for rather than restating it here:
    // a guard that only checks the planner's side passes while a renamed plan
    // fails at runtime on a missing file.
    const requested = Object.values(workflow.jobs).flatMap(job => (job.steps ?? []).flatMap(step =>
      [...(step.run ?? '').matchAll(/--plan "\$\{RUNNER_TEMP\}\/ci-plan\/([^"]+)"/g)]
        .map(match => match[1])));
    const phaseIds = workflow.jobs['codefly-quality-phases'].strategy.matrix.phase
      .map(phase => phase.replaceAll(',', '-'));
    // The quality matrix asks for its plan through ${CI_PHASE//,/-}; the build
    // names its own. Both must resolve to a file the planner published.
    assert.deepEqual(requested.sort(), ['${CI_PHASE//,/-}.json', 'build.json']);

    const published = readdirSync(join(temporary, 'ci-plan')).sort();
    const replayed = [...phaseIds, 'build'].map(id => `${id}.json`).sort();
    assert.deepEqual(published, replayed);

    // Each plan is built for the phase it is named after, and bound to the same
    // selection the planner resolved — a plan built for another phase, or under
    // other bounds, is refused at replay rather than silently narrowing the run.
    for (const id of replayed) {
      const emitted = readFileSync(join(temporary, 'ci-plan', id), 'utf8').trim();
      assert.equal(emitted, `ci plan --head head --base base --format json --replay --phase ${id.replace('.json', '')}`);
    }
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
});

// A record certifies a past success to a later run, so reuse is authenticated:
// without the signing key the CLI refuses outright, which would fail every fork
// pull request — GitHub gives those no secrets — rather than just execute.
test('results are reused only under a signing key, and only main is trusted to certify them', () => {
  const withoutKey = runQualityPhase('lint');
  assert.ok(!withoutKey.includes('--reuse'), withoutKey);

  // An unidentifiable execution environment disables reuse rather than matching
  // records against a placeholder identity two different machines would share.
  const withoutEnvironment = runQualityPhase('lint', { CODEFLY_CI_RESULT_KEY: 'key', CODEFLY_CI_REUSE_ENVIRONMENT: '' });
  assert.ok(!withoutEnvironment.includes('--reuse'), withoutEnvironment);

  const reused = runQualityPhase('lint', {
    CODEFLY_CI_RESULT_KEY: 'key',
    CODEFLY_CI_REUSE_ENVIRONMENT: 'image',
    GITHUB_REF_NAME: '748/merge',
    GITHUB_RUN_ID: '42',
    GITHUB_RUN_ATTEMPT: '1',
  });
  assert.ok(reused.includes('--reuse-results'), reused);
  assert.ok(reused.includes('--reuse-trusted-reference main'), reused);
  assert.ok(reused.includes('--reuse-environment image'), reused);
  // Published under this run's own reference, which is not the trusted one, so
  // the CLI consumes main's evidence here and refuses to write any.
  assert.ok(reused.includes('--reuse-reference 748/merge'), reused);
  assert.ok(reused.includes('--reuse-run 42/1'), reused);
  // Unchanged by reuse: the bounds and the plan still decide what runs.
  assert.ok(reused.startsWith('ci run --head head --base base --plan /runner-temp/ci-plan/lint.json'), reused);

  const save = workflow.jobs['codefly-quality-phases'].steps.find(step => step.name === 'Save verified CI results');
  assert.match(save.if, /github\.ref == 'refs\/heads\/main'/);
  assert.match(save.if, /steps\.reuse\.outputs\.enabled == 'true'/);
  // A failing phase has already published records for the services that passed.
  // Dropping the save on failure would discard them and make every run after a
  // red main re-execute verified work; `always()` would instead save from a
  // cancelled run, whose store is incomplete at an arbitrary point.
  assert.match(save.if, /!cancelled\(\)/);
  assert.ok(!/always\(\)/.test(save.if), save.if);
  // actions/cache rejects a key containing a comma; CI_PHASE_ID is the phase list with
  // commas replaced, so a multi-phase matrix entry can never leak one into the key.
  const restore = workflow.jobs['codefly-quality-phases'].steps.find(step => step.name === 'Restore verified CI results');
  for (const step of [save, restore]) {
    assert.ok(!step.with.key.includes(','), step.with.key);
    assert.ok(step.with.key.includes('env.CI_PHASE_ID'), step.with.key);
    assert.ok(step.with.key.includes('env.CODEFLY_CI_REUSE_ENVIRONMENT'), step.with.key);
  }
});

test('a failing quality command still fails its matrix job', () => {
  const script = workflow.jobs['codefly-quality-phases'].steps.find(step => step.name === 'Run quality phase').run;
  for (const phase of ['sync-drift', 'lint', 'compile', 'test']) {
    const result = spawnSync('bash', ['-e', '-c', `codefly() { return 17; };\n${script}`], {
      env: { ...process.env, CI_PHASE: phase, SELECTION_ALL: 'true', GITHUB_SHA: 'head', AFFECTED_SERVICES: 'frontend' },
    });
    assert.equal(result.status, 17);
  }
});

// Every task binds the digest of the agent binary it executes, so a job that
// runs one resolves each pinned agent before it can execute or reuse anything.
// Cold, that is a GitHub release fetch per agent: 20 and 26 downloads costing
// 70s and 89s across runs 35277937608 and 35255201609, up to 30s for a single
// agent. The planner spawns none and stays uncached.
test('every job that runs a service agent restores them from one keyed cache', () => {
  const step = job => (job.steps ?? []).find(candidate => candidate.name === 'Cache resolved service agents');
  const installs = Object.entries(workflow.jobs)
    .filter(([, job]) => (job.steps ?? []).some(candidate => candidate.run === 'bash scripts/ci/install-codefly.sh'));
  assert.deepEqual(installs.filter(([, job]) => step(job)).map(([name]) => name).sort(),
    ['codefly-build', 'codefly-quality-phases', 'codefly-supply-chain', 'sdk-boundary']);

  // The service manifests carry every agent pin, so a bump earns a new entry
  // and the fallback then restores the agents the bump did not touch
  // — the difference between one download and a full cold resolution. Both
  // halves carry the runner's architecture: the cached path carries an agent's
  // version but not its platform, so a fallback that crossed architectures
  // would restore a binary the CLI finds, runs, and cannot execute.
  const pins = "${{ hashFiles('module/services/*/service.codefly.yaml') }}";
  const fallback = '${{ runner.os }}-${{ runner.arch }}-';
  for (const [name, job] of installs.filter(([, candidate]) => step(candidate))) {
    const cache = step(job);
    assert.equal(cache.with.path, '~/.codefly/agents', name);
    assert.equal(cache.with['restore-keys'].trim(), `codefly-agents-${fallback}`, name);
    assert.equal(cache.with.key, `codefly-agents-${fallback}${pins}`, name);
  }
});

test('every buf setup authenticates, so no job resolves a release on the shared anonymous rate limit', () => {
  const steps = Object.values(workflow.jobs)
    .flatMap(job => job.steps ?? [])
    .filter(step => typeof step.uses === 'string' && step.uses.startsWith('bufbuild/buf-setup-action@'));
  assert.ok(steps.length > 0);
  for (const step of steps) {
    assert.equal(step.with?.github_token, '${{ secrets.GITHUB_TOKEN }}');
  }
});

test('disk preparation skips unnecessary deletion, stops when sufficient, and fails when exhausted', () => {
  const dir = mkdtempSync(join(tmpdir(), 'ci-disk-'));
  try {
    const log = join(dir, 'removed');
    writeFileSync(join(dir, 'df'), `#!/bin/bash
echo 'Filesystem 1024-blocks Used Available Capacity Mounted'
available="$INITIAL_SPACE"
if [[ "$RECOVER_SPACE" == true && -s "$DELETE_LOG" ]]; then available=20000000; fi
echo "disk 40000000 0 $available 0% /"
`, { mode: 0o755 });
    writeFileSync(join(dir, 'sudo'), '#!/bin/bash\nprintf "%s\\n" "$*" >> "$DELETE_LOG"\n', { mode: 0o755 });
    for (const [initial, recover, status, deletions] of [
      ['20000000', 'false', 0, 0], ['1000', 'true', 0, 1], ['1000', 'false', 1, 3],
    ]) {
      writeFileSync(log, '');
      const result = spawnSync('bash', [join(root, 'scripts/ci/ensure-disk-space.sh')], {
        encoding: 'utf8',
        env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, DELETE_LOG: log, INITIAL_SPACE: initial, RECOVER_SPACE: recover },
      });
      assert.equal(result.status, status, result.stderr);
      assert.equal(readFileSync(log, 'utf8').split('\n').filter(Boolean).length, deletions);
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
