import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { parseWorkflowYaml } from './workflow-yaml.mjs';

const root = join(import.meta.dirname, '../..');
const workflow = parseWorkflowYaml(readFileSync(join(root, '.github/workflows/ci.yml'), 'utf8'));

test('the CLI installer verifies downloads before putting the executable on PATH', () => {
  const dir = mkdtempSync(join(tmpdir(), 'ci-cli-'));
  try {
    writeFileSync(join(dir, 'codefly'), '#!/bin/sh\necho fixture\n');
    const archive = join(dir, 'fixture.tar.gz');
    assert.equal(spawnSync('tar', ['-czf', archive, '-C', dir, 'codefly']).status, 0);
    const digest = createHash('sha256').update(readFileSync(archive)).digest('hex');
    const installer = readFileSync(join(root, 'scripts/ci/install-codefly.sh'), 'utf8');
    writeFileSync(join(dir, 'installer.sh'), installer.replace(/^checksum=.+$/m, `checksum=${digest}`));
    writeFileSync(join(dir, 'uname'), '#!/bin/sh\nif [ "$1" = -s ]; then echo Linux; else echo x86_64; fi\n', { mode: 0o755 });
    writeFileSync(join(dir, 'curl'), '#!/bin/bash\ncp "$FIXTURE_ARCHIVE" "${@: -1}"\n', { mode: 0o755 });
    const pathFile = join(dir, 'github-path');
    const env = { ...process.env, PATH: `${dir}:${process.env.PATH}`, RUNNER_TEMP: dir, GITHUB_PATH: pathFile, FIXTURE_ARCHIVE: archive };
    const valid = spawnSync('bash', [join(dir, 'installer.sh')], { env, encoding: 'utf8' });
    assert.equal(valid.status, 0, valid.stderr);
    assert.equal(readFileSync(join(dir, 'codefly-bin/codefly'), 'utf8'), '#!/bin/sh\necho fixture\n');
    writeFileSync(pathFile, '');
    writeFileSync(archive, 'corrupt download');
    const corrupt = spawnSync('bash', [join(dir, 'installer.sh')], { env, encoding: 'utf8' });
    assert.notEqual(corrupt.status, 0);
    assert.equal(readFileSync(pathFile, 'utf8'), '');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
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

test('every quality phase runs independently with the original service selection', () => {
  const job = workflow.jobs['codefly-quality-phases'];
  assert.deepEqual(job.strategy.matrix.phase, ['verify,sync-drift', 'lint', 'compile', 'test']);
  assert.equal(job.strategy['fail-fast'], 'false');
  const script = job.steps.find(step => step.name === 'Run quality phase').run;
  for (const phase of job.strategy.matrix.phase) {
    const result = spawnSync('bash', ['-e', '-c', `codefly() { printf '%s\\n' "$*"; };\n${script}`], {
      encoding: 'utf8',
      env: { ...process.env, CI_PHASE: phase, SELECTION_ALL: 'false', CODEFLY_BASE: 'base', GITHUB_SHA: 'head', AFFECTED_SERVICES: 'accounts frontend' },
    });
    assert.equal(result.status, 0, result.stderr);
    const selection = phase === 'verify,sync-drift'
      ? '--head head --base base'
      : '--changed-file module/services/accounts/service.codefly.yaml --changed-file module/services/frontend/service.codefly.yaml';
    assert.ok(result.stdout.startsWith(`ci run ${selection} --phase ${phase} --output `), result.stdout);
    assert.equal(result.stdout.trim().split('\n').length, 1);
  }
});

test('a failing quality command still fails its matrix job', () => {
  const script = workflow.jobs['codefly-quality-phases'].steps.find(step => step.name === 'Run quality phase').run;
  for (const phase of ['verify,sync-drift', 'lint', 'compile', 'test']) {
    const result = spawnSync('bash', ['-e', '-c', `codefly() { return 17; };\n${script}`], {
      env: { ...process.env, CI_PHASE: phase, SELECTION_ALL: 'true', GITHUB_SHA: 'head', AFFECTED_SERVICES: 'frontend' },
    });
    assert.equal(result.status, 17);
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
