import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
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
      .replace(/^checksum=.+$/m, `checksum=${digest}`)
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

test('only the test phase installs the isolated-session source tool after disk preparation', () => {
  const job = workflow.jobs['codefly-quality-phases'];
  const release = job.steps.find(step => step.name === 'Install Codefly');
  const source = job.steps.find(step => step.name === 'Install isolated-session Codefly test tool');
  const disk = job.steps.findIndex(step => step.name === 'Ensure runner disk space');
  assert.equal(release.if, "matrix.phase != 'test'");
  assert.equal(release.run, 'bash scripts/ci/install-codefly.sh');
  assert.equal(source.if, "matrix.phase == 'test'");
  assert.equal(source.run, 'bash scripts/ci/install-codefly-test.sh');
  assert.ok(job.steps.indexOf(source) > disk);
  assert.equal(job['timeout-minutes'], '30');
  for (const [name, other] of Object.entries(workflow.jobs)) {
    if (name === 'codefly-quality-phases') continue;
    assert.ok(!JSON.stringify(other).includes('install-codefly-test.sh'), name);
  }
});

test('the source test-tool installer rejects wrong provenance or failed builds before exposing a tool', () => {
  const dir = mkdtempSync(join(tmpdir(), 'ci-cli-source-'));
  const commit = '7a3a895a1ea308ea838e9e165221fc875d2563bb';
  const tree = 'ea209937b139e02138990039985c684228d9af8d';
  try {
    writeFileSync(join(dir, 'uname'), '#!/bin/sh\nif [ "$1" = -s ]; then echo Linux; else echo x86_64; fi\n', { mode: 0o755 });
    writeFileSync(join(dir, 'git'), `#!/bin/bash
if [[ "$1" == init ]]; then mkdir -p "\${@: -1}"; exit; fi
if [[ "$3" == rev-parse ]]; then
  if [[ "$SCENARIO" == wrong-source ]]; then echo wrong; exit; fi
  if [[ "$4" == HEAD ]]; then echo '${commit}'; else echo '${tree}'; fi
fi
`, { mode: 0o755 });
    writeFileSync(join(dir, 'go'), `#!/bin/bash
if [[ "$1" == env ]]; then
  if [[ "$SCENARIO" == wrong-go ]]; then echo go1.26.0; else echo go1.27.0; fi
elif [[ "$1" == build ]]; then
  [[ "$SCENARIO" == build-failure ]] && exit 17
  while [[ "$1" != -o ]]; do shift; done
  printf '#!/bin/sh\\necho source-test-tool\\n' > "$2"
elif [[ "$1" == version ]]; then
  if [[ "$SCENARIO" == wrong-binary ]]; then echo vcs.revision=wrong; else echo vcs.revision=${commit}; fi
  echo vcs.modified=false
fi
`, { mode: 0o755 });
    for (const scenario of ['wrong-go', 'wrong-source', 'build-failure', 'wrong-binary', 'success']) {
      const pathFile = join(dir, 'github-path');
      writeFileSync(pathFile, '');
      const result = spawnSync('bash', [join(root, 'scripts/ci/install-codefly-test.sh')], {
        encoding: 'utf8',
        env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, RUNNER_TEMP: dir, GITHUB_PATH: pathFile, SCENARIO: scenario },
      });
      assert.equal(result.status === 0, scenario === 'success', `${scenario}: ${result.stderr}`);
      assert.equal(readFileSync(pathFile, 'utf8'), scenario === 'success' ? `${dir}/codefly-test-bin\n` : '');
    }
    const provenance = JSON.parse(readFileSync(join(dir, 'codefly-test-bin/provenance.json'), 'utf8'));
    assert.equal(provenance.commit, commit);
    assert.equal(provenance.tree, tree);
    assert.equal(provenance.binary_sha256, createHash('sha256').update(readFileSync(join(dir, 'codefly-test-bin/codefly'))).digest('hex'));
  } finally {
    rmSync(dir, { recursive: true, force: true });
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
