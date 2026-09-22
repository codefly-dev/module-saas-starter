import assert from 'node:assert/strict';
import { readFileSync, mkdtempSync, mkdirSync, writeFileSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';
import test from 'node:test';
import { materialImages, verifyImages, coverageErrors, monitor, selectBuild, buildImages, buildLog } from './build-images.mjs';
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

// These materials came from the released Go-gRPC recipe's hosted build. The
// Dockerfile frontend is executable build input, not just a recipe comment.
test('the adopted Go-gRPC image contract includes its exact Dockerfile frontend', () => {
  const inventory = JSON.parse(readFileSync(new URL('./build-images.json', import.meta.url), 'utf8'));
  const frontend = material('docker/dockerfile', '1', 'sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32');
  const materials = [
    frontend,
    material('golang', '1.27.0-alpine3.23', 'sha256:3747dcba41c8b0db3211fda4db61638b980e17ac5bb3c94460a975a9cfe19395'),
    material('alpine', '3.23.5', 'sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40'),
  ];
  assert.deepEqual(verifyImages(inventory['go-grpc'].images, materialImages(materials)), []);
  assert.match(verifyImages(inventory['go-grpc'].images, materialImages(materials.slice(1))).join('\n'),
    /Missing expected build material: docker\/dockerfile/);
  assert.match(verifyImages(inventory['go-grpc'].images, materialImages([
    { ...frontend, Digests: [digest] }, ...materials.slice(1),
  ])).join('\n'), /Unexpected build material: docker\/dockerfile/);
});

test('a recipe pinned by digest alone is still attributed to the release line it locks', () => {
  // BuildKit's own shape for `FROM alpine@sha256:…`: no version in the path, the
  // pin in a `digest` parameter. The Postgres migration builder pins this way.
  const pin = `sha256:${'c'.repeat(64)}`;
  const untagged = [{ URI: `pkg:docker/alpine?digest=${pin}&platform=linux%2Famd64`, Digests: [pin] }];
  assert.deepEqual(materialImages(untagged), [`alpine@${pin}`]);
  assert.deepEqual(verifyImages([`alpine:3.21.7@${pin}`], materialImages(untagged)), []);
  // The digest is the identity: a different one is not the locked base image.
  assert.match(verifyImages([`alpine:3.21.7@${digest}`], materialImages(untagged)).join('\n'), /Missing expected/);
  // Another repository cannot satisfy it, and an unpinned reference fails closed.
  assert.match(verifyImages([`busybox:1@${pin}`], materialImages(untagged)).join('\n'), /Missing expected/);
  assert.throws(() => materialImages([{ URI: 'pkg:docker/alpine?platform=linux%2Famd64', Digests: [pin] }]));
});

test('missing, malformed and overwritten material evidence fails closed', () => {
  for (const materials of [undefined, [], [{ URI: 'pkg:docker/node@24', Digests: [] }]]) assert.throws(() => materialImages(materials));
  assert.match(verifyImages([image], []).join('\n'), /Missing expected/);
  assert.match(verifyImages([image], materialImages([material('node', '26-alpine')])).join('\n'), /Unexpected/);
  assert.deepEqual(verifyImages(['alpine:3.21'], materialImages([material('alpine', '3.21')])), []);
});

test('build identity cannot borrow another service or recipe, or accept ambiguous or failed attempts', () => {
  const produced = 'example/frontend:0.0.0';
  const build = { Ref: 'gpb3pzuhyn5bmnc3zvmcu4aw1', Images: [produced], Status: 'completed' };
  assert.equal(selectBuild([build], produced), build);
  for (const builds of [[build, build], [{ ...build, Status: 'error' }], [{ ...build, Images: ['example/marketing:0.0.0'] }], [{ ...build, Images: [] }]]) {
    assert.throws(() => selectBuild(builds, produced));
  }
  // A count alone cannot tell a build that never ran from one whose exported
  // name the gate failed to read, and the builder reports the latter as passing.
  assert.throws(() => selectBuild([{ ...build, Images: [`${produced} 0.0s`] }], produced),
    /found 0 among 1 records since the snapshot\n {2}gpb3pzuhyn5bmnc3zvmcu4aw1 completed: example\/frontend:0\.0\.0 0\.0s$/);
  assert.throws(() => selectBuild([], produced), /found 0 among 0 records since the snapshot$/);
});

test('exported images are read from the record, and a build that exported nothing cannot pass as one', () => {
  assert.deepEqual(buildImages('#9 exporting to image\n#9 naming to docker.io/example/frontend:0.0.0 done\n'), ['example/frontend:0.0.0']);
  assert.deepEqual(buildImages('#9 naming to docker.io/library/alpine:3.21, ghcr.io/example/app:1 done\r\n'),
    ['alpine:3.21', 'ghcr.io/example/app:1']);
  // A printed build step must not be mistaken for BuildKit's own export line.
  assert.deepEqual(buildImages('#4 1.23 #9 naming to docker.io/example/forged:0.0.0 done\n#9 exporting layers\n'), []);
});

// BuildKit prints a status's elapsed time once it passes 10ms, so the identical
// build exports `naming to <ref> 0.0s done` on a busy runner and
// `naming to <ref> done` on an idle one. Reading that field as part of the
// reference left one service per run unattributable, at random.
test('an export line carrying its elapsed time names the same image as one without', () => {
  assert.deepEqual(buildImages('#11 naming to docker.io/codefly-dev/saas-starter-dev/saas-starter/store:0.0.0 0.0s done\n'),
    ['codefly-dev/saas-starter-dev/saas-starter/store:0.0.0']);
  assert.deepEqual(buildImages('#9 naming to docker.io/library/alpine:3.21, ghcr.io/example/app:1 12.5s done\r\n'),
    ['alpine:3.21', 'ghcr.io/example/app:1']);
  // The field is BuildKit's, not part of a reference a recipe could name.
  assert.deepEqual(buildImages('#9 naming to docker.io/example/app:0.0.0 0.0s done\n#9 naming to docker.io/example/app:0.0.0 done\n'),
    ['example/app:0.0.0']);
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

// `alpine@sha256:…` split on '@' yields the bare repository, and
// `imagetools inspect alpine` resolves alpine:latest — so a digest-pinned ref
// reads as permanently changed, against an image the pin never named.
test('the registry monitor never polls a bare repository for a pin that names no tag', () => {
  const polled = [];
  const probe = ref => { polled.push(ref); return digest; };
  const tagless = { example: { source: 'https://example.com/agent', images: [`alpine@${digest}`], watch: ['alpine:3.21'] } };
  assert.match(monitor(tagless, probe).join('\n'), /names no tag to poll upstream/);
  assert.deepEqual(polled, [], 'a pin with no tag is reported, never resolved as :latest');

  // The same base pinned as tag@digest polls that exact tag and stays quiet.
  const pinned = { example: { source: 'https://example.com/agent', images: [`alpine:3.21@${digest}`], watch: ['alpine:3.21'] } };
  assert.deepEqual(monitor(pinned, probe), []);
  assert.deepEqual(polled, ['alpine:3.21', 'alpine:3.21']);
  // A registry port is not a tag, so it is reported rather than polled.
  const port = { example: { source: 'https://example.com/agent', images: [`registry:5000/img@${digest}`], watch: [] } };
  assert.match(monitor(port, probe).join('\n'), /names no tag to poll upstream/);
});

// Nothing tied SUPPLY_CHAIN_SECURITY.md's pin claims to the bindings, so a pin
// bump left the auditor-facing document naming versions the repo no longer
// carried while every check stayed green. Each claim is now checkable prose.
test('every agent pin SUPPLY_CHAIN_SECURITY.md claims matches the topology bindings', () => {
  const doc = readFileSync(new URL('../../SUPPLY_CHAIN_SECURITY.md', import.meta.url), 'utf8');
  const pins = new Map(parseWorkflowYaml(readFileSync(
    new URL('../../module/deployment/topology.bindings.codefly.yaml', import.meta.url), 'utf8'),
  ).services.map(service => [service.name, service.agent.version]));
  // Markdown wraps these sentences, so match on collapsed whitespace.
  const claims = [...doc.replace(/\s+/g, ' ')
    .matchAll(/the `([a-z-]+)` pin is `(\d+\.\d+\.\d+)` in this repo/g)];
  assert.ok(claims.length >= 3, `expected a checkable pin claim per remediated service; found ${claims.length}`);
  for (const [, service, claimed] of claims) {
    assert.ok(pins.has(service), `SUPPLY_CHAIN_SECURITY.md claims a pin for unknown service ${service}`);
    assert.equal(claimed, pins.get(service),
      `SUPPLY_CHAIN_SECURITY.md says ${service} pins ${claimed}; topology bindings pin ${pins.get(service)}`);
  }
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
    const manifest = recipes => put('module/services/example/build-recipes/1.0.0/recipe.codefly.json',
      JSON.stringify({ schema: 'codefly.dev/build-recipe/v2', name: 'example', version: '1.0.0', recipes }));
    manifest([{ dockerfile: 'Dockerfile', context: '.', image: 'example/app:0.0.0' }]);
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
    // A recipe that names no image leaves nothing to attribute a build record
    // to, so it must fail rather than silently verify against no evidence.
    manifest([{ dockerfile: 'Dockerfile', context: '.' }]);
    assert.match(evidence().stderr, /does not name the image it produces/);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

// The `naming to` status crosses BuildKit's 10ms threshold only under load, so
// the elapsed field that breaks attribution in CI cannot be produced on demand.
// Its siblings in the same record do carry it and the printer formats every
// status alike, so one real record supplies both the grammar and a real token to
// attach to its own export line. A Buildx pin bump that moves that format fails
// here, rather than intermittently reporting `found 0` months later.
test('a real build record pins the elapsed field the export parser strips',
  { skip: process.env.BUILD_IMAGES_DOCKER_TEST !== 'true' }, () => {
    const dir = mkdtempSync(join(tmpdir(), 'build-image-elapsed-'));
    const image = 'codefly-dev/example-workspace/example/elapsed:0.0.0';
    try {
      // A layer this size always takes longer than 10ms to export; the marker
      // keeps a rerun from resolving the step from cache and exporting nothing.
      writeFileSync(join(dir, 'Dockerfile'),
        `FROM alpine:3.23.5\nRUN echo ${Date.now()} >/dev/null && dd if=/dev/urandom of=/blob bs=1M count=64 2>/dev/null\n`);
      const metadata = join(dir, 'metadata.json');
      execFileSync('docker', ['buildx', 'build', '--load', '--metadata-file', metadata, '-t', image, dir], { stdio: 'pipe' });
      const log = buildLog(JSON.parse(readFileSync(metadata))['buildx.build.ref'].split('/').at(-1));

      // Anchored on a status whose id is fixed, so whatever trails it is the
      // field itself and not something this test assumed about the format.
      const timed = [...log.matchAll(/^#\d+ exporting layers(.*) done$/gm)].map(match => match[1]).filter(Boolean);
      assert.ok(timed.length, 'BuildKit should time the export of a 64MB layer');
      for (const field of timed) assert.match(field, /^ \d+\.\d+s$/);

      const naming = log.match(/^#\d+ naming to .+$/m)[0];
      assert.deepEqual(buildImages(`${naming}\n`), [image]);
      assert.deepEqual(buildImages(`${naming.replace(/( done)?$/, `${timed[0]}$1`)}\n`), [image]);
    } finally {
      rmSync(dir, { recursive: true, force: true });
      spawnSync('docker', ['image', 'rm', image], { stdio: 'ignore' });
    }
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

// The CLI copies the recipe's Dockerfile into a temporary directory and builds
// the service tree from there, so a record's Dockerfile is a staged path that is
// deleted before the gate runs — `builder/Dockerfile` never appears in it. The
// context still names the service; only the exported image names the recipe.
test('a build whose staged definition is gone is still attributed to the recipe that exported the image',
  { skip: process.env.BUILD_IMAGES_DOCKER_TEST !== 'true' }, () => {
    const context = mkdtempSync(join(tmpdir(), 'build-image-service-'));
    const staged = mkdtempSync(join(tmpdir(), 'codefly-build-definition-'));
    const image = 'codefly-dev/example-workspace/example/staged:0.0.0';
    try {
      writeFileSync(join(staged, 'Dockerfile'), 'FROM alpine:3.23.5\n');
      const metadata = join(context, 'metadata.json');
      execFileSync('docker', ['buildx', 'build', '--load', '--metadata-file', metadata,
        '-f', join(staged, 'Dockerfile'), '-t', image, context], { stdio: 'pipe' });
      const ref = JSON.parse(readFileSync(metadata))['buildx.build.ref'].split('/').at(-1);
      const build = JSON.parse(execFileSync('docker', ['buildx', 'history', 'inspect', ref, '--format', 'json'], { encoding: 'utf8' }));
      rmSync(staged, { recursive: true, force: true });

      build.Images = buildImages(buildLog(ref));
      assert.deepEqual(build.Images, [image]);
      assert.equal(selectBuild([build], image), build);
      // The recipe path the gate used to match on never appears and is gone.
      assert.doesNotMatch(build.Dockerfile, /builder\/Dockerfile$/);
      assert.ok(!existsSync(build.Dockerfile));
      assert.deepEqual(verifyImages(['alpine:3.23.5'], materialImages(build.Materials)), []);
    } finally {
      rmSync(staged, { recursive: true, force: true });
      rmSync(context, { recursive: true, force: true });
      spawnSync('docker', ['image', 'rm', image], { stdio: 'ignore' });
    }
  });
