import assert from 'node:assert/strict';
import test from 'node:test';
import { referenceErrors, needsReplay } from './migration-reference-gate.mjs';
const path = n => `module/services/store/migrations/${n}_example.up.sql`;
const tree = (...versions) => new Map(versions.map(n => [path(n), 'body']));

test('new versions must exceed the current reference, including queue predecessors', () => {
  assert.deepEqual(referenceErrors(tree(131), tree(131, 134)), []);
  assert.match(referenceErrors(tree(131), tree(123, 131))[0], /frontier 131/);
  assert.match(referenceErrors(tree(131, 135), tree(131, 134, 135))[0], /frontier 135/);
  assert.match(referenceErrors(tree(131), tree(131, '0131'))[0], /frontier 131/);
});
test('shipped bodies cannot be edited, deleted, or renamed', () => {
  for (const after of [new Map([[path(131), 'edited']]), tree(), tree(132)]) {
    assert.match(referenceErrors(tree(131), after)[0], /shipped migration changed or deleted/);
  }
});
test('frontiers are independent per service', () => {
  const after = tree(131);
  after.set('module/services/example/migrations/1_example.up.sql', 'body');
  assert.deepEqual(referenceErrors(tree(131), after), []);
});
test('replay covers migrations, runner, dependency pins, and the gate itself', () => {
  for (const path of ['module/services/store/migrations/136_example.up.sql', 'module/services/store/code/main.go', 'module/services/store/code/go.mod', 'module/tools/migration-reference-gate.mjs', '.github/workflows/ci.yml', 'module/deployment/topology.bindings.codefly.yaml']) assert.equal(needsReplay([path]), true, path);
  assert.equal(needsReplay(['module/services/frontend/code/src/app/page.tsx', 'RELEASE_GATES.md']), false);
});

test('required CI context validates the event base and only scopes database replay', async () => {
  const { readFileSync } = await import('node:fs');
  const { parseWorkflowYaml } = await import('../../scripts/ci/workflow-yaml.mjs');
  const workflow = parseWorkflowYaml(readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8'));
  assert.ok(Object.hasOwn(workflow.on, 'pull_request'));
  assert.ok(Object.hasOwn(workflow.on, 'merge_group'));
  const job = workflow.jobs['base-integrity'];
  assert.equal(job.name, 'Base manifest integrity');
  assert.equal(job.if, undefined);
  const check = job.steps.find(step => step.id === 'migration-reference');
  assert.equal(check.if, undefined);
  assert.match(check.env.MIGRATION_BASE, /github.event.pull_request.base.sha \|\| github.event.merge_group.base_sha/);
  const replay = job.steps.find(step => step.run?.includes('--replay'));
  assert.equal(replay.if, "steps.migration-reference.outputs.replay == 'true'");
  assert.equal(replay.env.MIGRATION_BASE, check.env.MIGRATION_BASE);
  assert.equal(job.steps.find(step => step.uses?.startsWith('actions/checkout@')).with['fetch-depth'], '0');
});

test('the store target catalog declares every tracked path the gate would replay', async () => {
  const { readFileSync } = await import('node:fs');
  const { execFileSync } = await import('node:child_process');
  const { fileURLToPath } = await import('node:url');
  const repo = fileURLToPath(new URL('../../', import.meta.url));
  const catalog = JSON.parse(readFileSync(new URL('../services/store/test-targets.json', import.meta.url), 'utf8'));
  const target = suite => catalog.targets.find(entry => entry.suite === suite);
  const reference = target('migration-reference');
  const qualification = target('migration-qualification');
  const covered = path => qualification.input_roots.some(root => root.endsWith('/') ? path.startsWith(root) : root === path);
  const sample = root => root.endsWith('/') ? `${root}probe` : root;
  // Exhaustive over the tracked tree in both directions, so widening the predicate
  // without widening the declaration fails here rather than silently under-selecting.
  const tracked = execFileSync('git', ['ls-files'], { cwd: repo, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 }).trim().split('\n');
  assert.deepEqual(tracked.filter(path => needsReplay([path]) && !covered(path)), [], 'undeclared replay inputs');
  for (const root of qualification.input_roots) assert.equal(needsReplay([sample(root)]), true, root);
  for (const root of reference.input_roots) assert.ok(covered(root), root);
  // The image and the readiness budget belong to the upgrade test. Restating them
  // here would let the catalog assert a value nothing runs.
  const upgrade = readFileSync(new URL('../services/store/code/migration_upgrade_test.go', import.meta.url), 'utf8');
  const images = [...new Set([...upgrade.matchAll(/"(postgres:[\w.]+)"/g)].map(match => match[1]))];
  assert.deepEqual(qualification.external_inputs.map(input => input.name), images);
  const deadline = /deadline := time\.Now\(\)\.Add\((\d+) \* time\.Second\)/.exec(upgrade);
  assert.ok(deadline, 'readiness deadline literal not found in the upgrade test');
  assert.equal(qualification.setup_budget_seconds, Number(deadline[1]));
  assert.deepEqual(reference.external_inputs, []);
  assert.equal(reference.setup_budget_seconds, 0);
  for (const entry of catalog.targets) assert.deepEqual(entry.runtime_services, [], entry.suite);
});
