import assert from 'node:assert/strict';
import { readFileSync, mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';
import test from 'node:test';
import { externalImages, resolvedImages, verifyImages, coverageErrors, monitor } from './build-images.mjs';
import { parseWorkflowYaml } from './workflow-yaml.mjs';

const digest = `sha256:${'a'.repeat(64)}`;
const image = `node:24-alpine@${digest}`;
const workflow = parseWorkflowYaml(readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8'));

test('external images exclude stage aliases and scratch, preserving tags and digests', () => {
  assert.deepEqual(externalImages(`FROM --platform=linux/amd64 ${image} AS base\nFROM base AS build\nFROM scratch\nFROM BASE AS runner`), [image]);
  assert.deepEqual(externalImages('FROM alpine:3.21'), ['alpine:3.21']);
});

test('unresolved and missing image declarations fail closed', () => {
  for (const recipe of ['FROM ${BASE}', 'FROM alpine AS build extra', 'COPY . .']) {
    assert.throws(() => externalImages(recipe));
  }
});

test('only actual BuildKit FROM records establish resolved digest evidence', () => {
  assert.deepEqual(resolvedImages(`#2 load metadata for docker.io/library/${image}\n#3 resolve docker.io/library/${image}`), []);
  assert.deepEqual(resolvedImages(`#4 [base 1/1] FROM docker.io/library/${image}\n#4 [base 1/1] FROM docker.io/library/${image}`), [image]);
});

test('an upgrade overwritten by the agent fails even when the old image built successfully', () => {
  const proposed = `node:26-alpine@sha256:${'b'.repeat(64)}`;
  assert.match(verifyImages([proposed], [image], [image]).join('\n'), /agent generated node:24/);
  assert.deepEqual(verifyImages([proposed], [proposed], [proposed]), []);
});

test('a pinned recipe requires the exact build digest, including on cached builds', () => {
  assert.deepEqual(verifyImages([image], [image], [image]), []);
  assert.match(verifyImages([image], [image], []).join('\n'), /No BuildKit/);
  assert.match(verifyImages([image], [image], [`node:24-alpine@sha256:${'b'.repeat(64)}`]).join('\n'), /No BuildKit/);
});

test('a floating recipe records the digest resolved by the build, not a later registry lookup', () => {
  const ref = 'alpine:3.21';
  assert.deepEqual(verifyImages([ref], [ref], [`${ref}@${digest}`]), []);
  assert.match(verifyImages([ref], [ref], [`alpine:3.23@${digest}`]).join('\n'), /No BuildKit/);
});

test('new generated recipes cannot silently lose image dependency coverage', () => {
  const recipes = ['module/services/example/builder/Dockerfile'];
  const services = [{ name: 'example', agent: { name: 'example' } }];
  assert.equal(coverageErrors({}, services, recipes).length, 1);
  assert.deepEqual(coverageErrors({ example: { images: [image] } }, services, recipes), []);
});

test('CI verifies regenerated recipes after the canonical build and retains evidence on failure', () => {
  const steps = workflow.jobs['codefly-build'].steps;
  const build = steps.findIndex(step => step.name === 'Build affected service images');
  const verify = steps.findIndex(step => step.run === 'node scripts/ci/build-images.mjs evidence');
  assert.ok(verify > build);
  assert.match(steps[build].run, /codefly ci run/);
  assert.match(steps[build].run, /build_log=".codefly\/ci\/build.log"/);
  assert.equal(steps[build].env.BUILDKIT_PROGRESS, 'plain');
  assert.match(steps[verify].if, /!cancelled/);
  const upload = steps.find(step => step.uses?.startsWith('actions/upload-artifact@'));
  assert.match(upload.if, /!cancelled/);
  assert.equal(upload.with['include-hidden-files'], 'true');
});

test('ineffective proposals stop the plan before expensive service CI and contract changes qualify all services', () => {
  const steps = workflow.jobs['codefly-plan'].steps;
  assert.ok(steps.findIndex(step => step.run === 'node scripts/ci/build-images.mjs check') <
    steps.findIndex(step => step.name === 'Install Codefly'));
  const plan = steps.find(step => step.name === 'Resolve affected services').run;
  assert.match(plan, /git diff --name-only.*scripts\/ci\/build-images.json/);
  assert.match(plan, /all=true\n\s+selection_args\+=\(--all\)/);
});


test('the registry monitor reports digest changes and release-line upgrades at the owning source', () => {
  const config = { example: { source: 'https://example.com/agent', images: [image], watch: ['node:alpine'] } };
  assert.deepEqual(monitor(config, () => digest), []);
  const errors = monitor(config, () => `sha256:${'b'.repeat(64)}`);
  assert.match(errors.join('\n'), /digest changed/);
  assert.match(errors.join('\n'), /review node:alpine in https:\/\/example.com\/agent/);
  assert.throws(() => monitor(config, () => { throw new Error('registry unavailable'); }), /registry unavailable/);
});

test('proposal checks use the PR base and require image changes to travel with topology adoption', () => {
  const dir = mkdtempSync(join(tmpdir(), 'build-image-proposal-'));
  const put = (path, content) => {
    mkdirSync(join(dir, path, '..'), { recursive: true });
    writeFileSync(join(dir, path), content);
  };
  const git = (...args) => execFileSync('git', ['-c', 'user.name=Acme', '-c', 'user.email=user@example.com', ...args], { cwd: dir, encoding: 'utf8' });
  const commit = () => { git('add', '.'); git('commit', '-qm', 'test: image proposal'); };
  const check = base => spawnSync(process.execPath, ['scripts/ci/build-images.mjs', 'check'], {
    cwd: dir, encoding: 'utf8', env: { ...process.env, CODEFLY_BASE: base },
  });
  try {
    for (const file of ['build-images.mjs', 'dependabot-coverage.mjs', 'workflow-yaml.mjs']) {
      put(`scripts/ci/${file}`, readFileSync(new URL(file, import.meta.url), 'utf8'));
    }
    const bindings = 'module/deployment/topology.bindings.codefly.yaml';
    put(bindings, 'services:\n  - name: example\n    agent:\n      name: example\n      version: 1.0.0\n');
    const recipe = 'module/services/example/builder/Dockerfile';
    put(recipe, `FROM ${image}\n`);
    git('init', '-q');
    commit();
    const initial = git('rev-parse', 'HEAD').trim();
    const inventory = 'scripts/ci/build-images.json';
    put(inventory, JSON.stringify({ example: { images: [image] } }));
    commit();
    assert.equal(check(initial).status, 0);
    const base = git('rev-parse', 'HEAD').trim();
    put(recipe, `# refreshed\nFROM ${image}\n`);
    commit();
    assert.match(check(base).stderr, /edits alone cannot change the build/);
    const upgraded = `node:26-alpine@${digest}`;
    put(recipe, `FROM ${upgraded}\n`);
    commit();
    assert.match(check(base).stderr, /proposed images differ/);
    put(inventory, JSON.stringify({ example: { images: [upgraded] } }));
    commit();
    assert.match(check(base).stderr, /require an agent release adopted through topology bindings/);
    put(bindings, readFileSync(join(dir, bindings), 'utf8').replace('1.0.0', '2.0.0'));
    commit();
    assert.equal(check(base).status, 0, check(base).stderr);
    const recipeDir = 'module/services/example/build-recipes/2.0.0';
    put(`${recipeDir}/recipe.codefly.json`, JSON.stringify({ schema: 'codefly.dev/build-recipe/v2', name: 'example', version: '2.0.0', recipes: [{ dockerfile: 'builder/Dockerfile' }] }));
    put(`${recipeDir}/builder/Dockerfile`, `FROM ${upgraded}\n`);
    put('.codefly/ci/build.log', `#3 [build 1/1] FROM docker.io/library/${upgraded}\n`);
    const evidence = () => spawnSync(process.execPath, ['scripts/ci/build-images.mjs', 'evidence'], {
      cwd: dir, encoding: 'utf8', env: { ...process.env, SELECTION_ALL: 'true' },
    });
    assert.equal(evidence().status, 0, evidence().stderr);
    const report = () => JSON.parse(readFileSync(join(dir, '.codefly/ci/build-images.json'), 'utf8'));
    assert.deepEqual(report().records[0].resolved, [upgraded]);
    put(`${recipeDir}/builder/Dockerfile`, `FROM ${image}\n`);
    put('.codefly/ci/build.log', `#3 [build 1/1] FROM docker.io/library/${image}\n`);
    assert.notEqual(evidence().status, 0);
    assert.match(report().records[0].errors.join('\n'), /agent generated/);

  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
