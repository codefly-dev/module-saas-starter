#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { appendFileSync, existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { migrationPairingErrors } from './migration-pairing-gate.mjs';

const migration = /^module\/services\/([^/]+)\/migrations\/(\d+)_.+\.(up|down)\.sql$/;
const git = (...args) => execFileSync('git', args, { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 });

// A fold replaces the whole ledger with one regenerated baseline. It is legal only
// when declared: the provenance file records the sha256 of every file it folded,
// and the reference must consist of exactly those files (by content), none of which
// survives. Anything less is an edit or a deletion of a shipped migration.
export function declaredFold(before, after, provenance, contentHash) {
  const replaced = provenance?.replaced_sha256;
  if (!replaced || before.size === 0) return false;
  for (const path of before.keys()) {
    if (after.has(path) || replaced[path] === undefined || contentHash(path) !== replaced[path]) return false;
  }
  return true;
}

export function referenceErrors(before, after, fold = false) {
  const errors = [];
  const frontier = new Map();
  for (const [path, hash] of before) {
    const [, service, version] = migration.exec(path);
    const n = BigInt(version);
    if (n > (frontier.get(service) ?? -1n)) frontier.set(service, n);
    if (fold) continue;
    if (after.get(path) !== hash) errors.push(`${path}: shipped migration changed or deleted; add a forward migration`);
  }
  if (fold) frontier.clear();
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
    // tools/ regenerates the baseline, builder/ carries the image recipe plus its
    // runtime-access SQL, and the provenance file declares a fold. Each changes
    // the schema a clean install produces, so each must replay.
    /^module\/services\/store\/(tools|builder)\//.test(path) ||
    path === 'module/services/store/baseline.provenance.json' ||
    /^module\/tools\/migration-/.test(path) ||
    path === '.github/workflows/ci.yml');
}

function main() {
  const argument = process.argv[2];
  const reference = /^0+$/.test(argument ?? '') ? 'HEAD' : argument;
  if (!reference) throw new Error('usage: migration-reference-gate.mjs <reference-sha> [--replay]');
  const before = tree(reference);
  const after = tree('HEAD');
  const provenancePath = 'module/services/store/baseline.provenance.json';
  const provenance = existsSync(provenancePath) ? JSON.parse(readFileSync(provenancePath, 'utf8')) : null;
  const fold = declaredFold(before, after, provenance,
    path => createHash('sha256').update(git('show', `${reference}:${path}`)).digest('hex'));
  if (fold) console.log(`Migration reference ${reference}: the ledger is folded into a regenerated baseline (declared by ${provenancePath})`);
  const errors = [...migrationPairingErrors(), ...referenceErrors(before, after, fold)];
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
