import assert from 'node:assert/strict';
import { readFileSync, mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';
import test from 'node:test';
import { materialImages, verifyImages, coverageErrors, monitor, selectBuild } from './build-images.mjs';
import { parseWorkflowYaml } from './workflow-yaml.mjs';

const digest = `sha256:${'a'.repeat(64)}`;
const image = `node:24-alpine@${digest}`;
const material = (name, version, hash = digest) => ({ URI: `pkg:docker/${name}@${version}?platform=linux%2Famd64`, Digests: [hash] });
const workflow = parseWorkflowYaml(readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8'));

test('executor materials preserve all effective dependencies and platform digests', () => {
  assert.deepEqual(materialImages([material('node', '24-alpine')]), [image]);
  assert.deepEqual(materialImages([material('docker.io/library/alpine', '3.21')]), [`alpine:3.21@${digest}`]);
  assert.match(verifyImages([image], materialImages([material('node', '24-alpine'), material('alpine', '3.21')])).join('\n'), /Unexpected build material/);
});

test('missing, malformed and overwritten material evidence fails closed', () => {
  for (const materials of [undefined, [], [{ URI: 'pkg:docker/node@24', Digests: [] }]]) assert.throws(() => materialImages(materials));
  assert.match(verifyImages([image], []).join('\n'), /Missing expected/);
  assert.match(verifyImages([image], materialImages([material('node', '26-alpine')])).join('\n'), /Unexpected/);
  assert.deepEqual(verifyImages(['alpine:3.21'], materialImages([material('alpine', '3.21')])), []);
});

test('build identity cannot borrow another service or recipe, or accept ambiguous or failed attempts', () => {
  const build = { Context: '/example/frontend', Dockerfile: 'builder/Dockerfile', Status: 'completed' };
  assert.equal(selectBuild([build], build.Context, build.Dockerfile), build);
  for (const builds of [[], [build, build], [{ ...build, Status: 'error' }], [{ ...build, Context: '/example/marketing' }], [{ ...build, Dockerfile: 'other' }]]) {
    assert.throws(() => selectBuild(builds, build.Context, build.Dockerfile));
  }
});

test('every topology agent needs coverage even before it emits a Dockerfile', () => {
  const services = [{ name: 'example', agent: { name: 'example' } }];
  assert.equal(coverageErrors({}, services, []).length, 1);
  assert.deepEqual(coverageErrors({ example: { images: [image] } }, services, []), []);
  const inventory = { example: { coverage: 'codefly-vendor-audit' } };
  assert.deepEqual(coverageErrors(inventory, services, []), []);
  assert.equal(coverageErrors(inventory, services, ['module/services/example/builder/Dockerfile']).length, 1);
});

test('CI snapshots each attempt before the canonical build and retains evidence on failure', () => {
  const steps = workflow.jobs['codefly-build'].steps;
  const script = steps.find(step => step.name === 'Build affected service images').run;
  assert.match(script, /for attempt.*\n\s+node scripts\/ci\/build-images.mjs snapshot\n\s+if codefly ci run/);
  const buildx = steps.find(step => step.uses?.startsWith('docker/setup-buildx-action@'));
  assert.equal(buildx.with.version, 'v0.33.0');
  assert.equal(buildx.with.driver, 'docker');
  assert.match(steps.find(step => step.run === 'node scripts/ci/build-images.mjs evidence').if, /!cancelled/);
  assert.equal(steps.find(step => step.uses?.startsWith('actions/upload-artifact@')).with['include-hidden-files'], 'true');
});

test('the registry monitor reports changed digests and skips only classified vendor agents', () => {
  const config = { example: { source: 'https://example.com/agent', images: [image], watch: ['node:alpine'] }, vendor: { coverage: 'codefly-vendor-audit' } };
  assert.deepEqual(monitor(config, () => digest), []);
  assert.match(monitor(config, () => `sha256:${'b'.repeat(64)}`).join('\n'), /digest changed/);
});

test('Git proposal checks allow deleting obsolete recipes and removing services but reject generated edits', () => {
  const dir = mkdtempSync(join(tmpdir(), 'build-image-proposal-'));
  const put = (path, content) => {
    mkdirSync(join(dir, path, '..'), { recursive: true });
    writeFileSync(join(dir, path), content);
  };
  const git = (...args) => execFileSync('git', ['-c', 'user.name=Acme', '-c', 'user.email=user@example.com', ...args], { cwd: dir, encoding: 'utf8' });
  const commit = () => { git('add', '.'); git('commit', '-qm', 'test: image proposal'); };
  const check = base => spawnSync(process.execPath, ['scripts/ci/build-images.mjs', 'check'], { cwd: dir, encoding: 'utf8', env: { ...process.env, CODEFLY_BASE: base } });
  try {
    for (const file of ['build-images.mjs', 'dependabot-coverage.mjs', 'workflow-yaml.mjs']) put(`scripts/ci/${file}`, readFileSync(new URL(file, import.meta.url), 'utf8'));
    const bindings = 'module/deployment/topology.bindings.codefly.yaml';
    put(bindings, 'services:\n  - name: example\n    agent:\n      name: example\n      version: 1.0.0\n');
    const recipe = 'module/services/example/builder/Dockerfile';
    put(recipe, `FROM ${image}\n`);
    put('scripts/ci/build-images.json', JSON.stringify({ example: { images: [image] } }));
    git('init', '-q'); commit();
    const base = git('rev-parse', 'HEAD').trim();
    put(recipe, ` FROM ${image}\n`); commit();
    assert.match(check(base).stderr, /generated recipes are not upgrade inputs/);
    rmSync(join(dir, recipe)); commit();
    assert.equal(check(base).status, 0, check(base).stderr);
    put(bindings, 'services: []\n'); commit();
    assert.equal(check(base).status, 0, check(base).stderr);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('evidence refuses missing coverage and excludes records from before this build attempt', () => {
  const dir = mkdtempSync(join(tmpdir(), 'build-image-evidence-'));
  const put = (path, content) => {
    mkdirSync(join(dir, path, '..'), { recursive: true });
    writeFileSync(join(dir, path), content);
  };
  try {
    for (const file of ['build-images.mjs', 'dependabot-coverage.mjs', 'workflow-yaml.mjs']) put(`scripts/ci/${file}`, readFileSync(new URL(file, import.meta.url), 'utf8'));
    put('module/deployment/topology.bindings.codefly.yaml', 'services:\n  - name: example\n    agent:\n      name: example\n      version: 1.0.0\n');
    put('scripts/ci/build-images.json', '{}');
    put('.codefly/ci/build-history-before.json', '["builder/node/old"]');
    put('module/services/example/build-recipes/1.0.0/recipe.codefly.json', JSON.stringify({ schema: 'codefly.dev/build-recipe/v2', name: 'example', version: '1.0.0', recipes: [{ dockerfile: 'Dockerfile', context: '.' }] }));
    put('module/services/example/build-recipes/1.0.0/Dockerfile', `FROM ${image}\n`);
    put('docker', `#!/usr/bin/env node
if (process.argv[4] === 'ls') console.log(JSON.stringify({ref:'builder/node/old'}));
else throw new Error('A stale record must never be inspected');
`);
    execFileSync('chmod', ['+x', join(dir, 'docker')]);
    const evidence = () => spawnSync(process.execPath, ['scripts/ci/build-images.mjs', 'evidence'], {
      cwd: dir, encoding: 'utf8', env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, SELECTION_ALL: 'true' },
    });
    assert.match(evidence().stderr, /No effective image coverage/);
    put('scripts/ci/build-images.json', JSON.stringify({ example: { coverage: 'codefly-vendor-audit' } }));
    assert.match(evidence().stderr, /No effective image coverage/);
    put('scripts/ci/build-images.json', JSON.stringify({ example: { images: [image] } }));
    assert.match(evidence().stderr, /Expected one completed build/);
    const report = JSON.parse(readFileSync(join(dir, '.codefly/ci/build-images.json'), 'utf8'));
    assert.equal(report.records.length, 1);
    assert.match(report.records[0].errors[0], /found 0/);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('real BuildKit materials handle whitespace, skip unreachable stages and ignore printed FROM lines', { skip: process.env.BUILD_IMAGES_DOCKER_TEST !== 'true' }, () => {
  const dir = mkdtempSync(join(tmpdir(), 'build-image-materials-'));
  const docker = (...args) => execFileSync('docker', ['buildx', ...args], { encoding: 'utf8' });
  const build = recipe => {
    writeFileSync(join(dir, 'Dockerfile'), recipe);
    const metadata = join(dir, 'metadata.json');
    execFileSync('docker', ['buildx', 'build', '--metadata-file', metadata, dir], { env: { ...process.env, BUILDX_METADATA_PROVENANCE: 'max' }, stdio: 'pipe' });
    const ref = JSON.parse(readFileSync(metadata))['buildx.build.ref'];
    return JSON.parse(docker('history', 'inspect', ref.split('/').at(-1), '--format', 'json'));
  };
  try {
    const hidden = build('FROM alpine:3.23.5 AS base\n FROM alpine:3.21 AS runner\nCOPY --from=base /etc/alpine-release /build-release\n');
    assert.match(verifyImages(['alpine:3.23.5'], materialImages(hidden.Materials)).join('\n'), /Unexpected build material: alpine:3.21/);
    const unused = build('FROM alpine:3.21 AS discarded\nFROM alpine:3.23.5\nRUN echo "FROM alpine:3.21@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"\n');
    assert.deepEqual(verifyImages(['alpine:3.23.5'], materialImages(unused.Materials)), []);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});
