#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import { appendFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { migrationPairingErrors } from './migration-pairing-gate.mjs';

const migration = /^module\/services\/([^/]+)\/migrations\/(\d+)_.+\.(up|down)\.sql$/;
const git = (...args) => execFileSync('git', args, { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 });

export function referenceErrors(before, after) {
  const errors = [];
  const frontier = new Map();
  for (const [path, hash] of before) {
    const [, service, version] = migration.exec(path);
    const n = BigInt(version);
    if (n > (frontier.get(service) ?? -1n)) frontier.set(service, n);
    if (after.get(path) !== hash) errors.push(`${path}: shipped migration changed or deleted; add a forward migration`);
  }
  for (const path of after.keys()) {
    if (before.has(path)) continue;
    const [, service, version] = migration.exec(path);
    if (BigInt(version) <= (frontier.get(service) ?? -1n)) {
      errors.push(`${path}: new version must exceed reference frontier ${frontier.get(service)}; renumber after earlier schema changes merge`);
    }
  }
  return errors;
}

function tree(ref) {
  return new Map(git('ls-tree', '-r', ref, '--', 'module/services').trim().split('\n').flatMap(line => {
    const [metadata, path] = line.split('\t');
    return migration.test(path ?? '') ? [[path, metadata.split(' ')[2]]] : [];
  }));
}

export function needsReplay(paths) {
  return paths.some(path => /^module\/services\/[^/]+\/migrations\//.test(path) ||
    /^module\/services\/store\/(code\/|.*codefly\.yaml$)/.test(path) ||
    /^module\/tools\/migration-/.test(path) ||
    path === 'module/deployment/topology.bindings.codefly.yaml' || path === '.github/workflows/ci.yml');
}

function main() {
  const argument = process.argv[2];
  const reference = /^0+$/.test(argument ?? '') ? 'HEAD' : argument;
  if (!reference) throw new Error('usage: migration-reference-gate.mjs <reference-sha> [--replay]');
  const errors = [...migrationPairingErrors(), ...referenceErrors(tree(reference), tree('HEAD'))];
  if (errors.length) throw new Error(errors.join('\n'));
  const replay = needsReplay(git('diff', '--name-only', reference, 'HEAD').trim().split('\n'));
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `replay=${replay}\n`);
  console.log(`Migration reference ${reference}: valid; database replay required: ${replay}`);
  if (!process.argv.includes('--replay')) return;
  const temp = mkdtempSync(join(tmpdir(), 'migration-reference-'));
  try {
    const archive = execFileSync('git', ['archive', reference, 'module/services/store/migrations'], { maxBuffer: 32 * 1024 * 1024 });
    execFileSync('tar', ['-x', '-C', temp], { input: archive });
    execFileSync('go', ['test', '-count=1', '-v', '-run', '^TestMigrationUpgrade$', '.'], {
      cwd: 'module/services/store/code', stdio: 'inherit',
      env: { ...process.env, MIGRATION_REFERENCE: join(temp, 'module/services/store/migrations') },
    });
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
}

if (resolve(process.argv[1] ?? '') === resolve(import.meta.filename)) main();
