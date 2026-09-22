import assert from 'node:assert/strict';
import test from 'node:test';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { catalogErrors } from './test-targets-gate.mjs';

const target = overrides => ({
  suite: 'unit', phase: 'test', command: ['go', 'test', './...'], runtime_services: [],
  setup_budget_seconds: 0, input_roots: ['src/'], complete: false, ...overrides,
});
const errorsFor = (overrides, catalogOverrides) => {
  const root = mkdtempSync(join(tmpdir(), 'test-targets-'));
  try {
    mkdirSync(join(root, 'module/services/example'), { recursive: true });
    mkdirSync(join(root, 'src'), { recursive: true });
    writeFileSync(join(root, 'file.txt'), 'x');
    writeFileSync(join(root, 'module/services/example/test-targets.json'), JSON.stringify({
      schema_version: 1, path_base: 'repository', working_directory: '.',
      fallback: 'whole-service-and-dependency-closure', targets: [target(overrides)], ...catalogOverrides,
    }));
    return catalogErrors(join(root, 'module')).join('\n');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
};

test('a conforming catalog passes', () => {
  assert.equal(errorsFor({}), '');
});
test('a bespoke key outside the shared schema fails', () => {
  assert.match(errorsFor({ ephemeral_images: ['postgres:16'] }), /key ephemeral_images is not in the shared schema/);
});
test('input roots must exist, and a directory root must end with a slash', () => {
  assert.match(errorsFor({ input_roots: ['does-not-exist/'] }), /is not a directory/);
  assert.match(errorsFor({ input_roots: ['src'] }), /directory roots must end with \//);
  assert.match(errorsFor({ input_roots: ['file.txt', 'file.txt'] }), /contains duplicates/);
});
test('command tokens and argument declarations must correspond', () => {
  assert.match(errorsFor({ command: ['node', 'run.mjs', '${reference}'] }), /token \$\{reference\} has no arguments entry/);
  assert.match(errorsFor({ arguments: { reference: 'unused' } }), /arguments entry reference is never used/);
});
test('a command naming a path that does not exist fails', () => {
  assert.match(errorsFor({ command: ['node', 'module/tools/absent.mjs'] }), /command path module\/tools\/absent\.mjs does not exist/);
});
test('a target that starts no dependency cannot carry a setup budget', () => {
  assert.match(errorsFor({ setup_budget_seconds: 30 }), /starts no dependency/);
  assert.equal(errorsFor({ setup_budget_seconds: 30, runtime_services: ['store'] }), '');
});
test('external inputs use Core vocabulary and never claim a bare digest', () => {
  assert.match(errorsFor({ external_inputs: [{ kind: 'IMAGE', name: 'postgres:16', identity: 'unresolved' }], setup_budget_seconds: 30 }), /kind IMAGE is not one of/);
  assert.match(errorsFor({ external_inputs: [{ kind: 'EXTERNAL', name: 'postgres:16', identity: '16' }], setup_budget_seconds: 30 }), /identity must be/);
});
test('catalog-level invariants are enforced', () => {
  assert.match(errorsFor({}, { schema_version: 2 }), /schema_version 2 is not 1/);
  assert.match(errorsFor({}, { working_directory: 'nowhere' }), /working_directory nowhere is not a directory/);
  assert.match(errorsFor({}, { fallback: 'none' }), /must be whole-service-and-dependency-closure/);
  assert.match(errorsFor({}, { extra: true }), /top-level keys .* must be exactly/);
});
