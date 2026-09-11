import { readFileSync, writeFileSync, appendFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { resolve } from 'node:path';
import { parseWorkflowYaml } from './workflow-yaml.mjs';
import { discoverManifests, isGeneratedRecipe } from './dependabot-coverage.mjs';

const root = resolve(import.meta.dirname, '../..');
const read = path => readFileSync(resolve(root, path), 'utf8');
const inventoryPath = 'scripts/ci/build-images.json';
const bindingsPath = 'module/deployment/topology.bindings.codefly.yaml';
const git = (...args) => execFileSync('git', args, { cwd: root, encoding: 'utf8' });
const normalize = ref => ref.replace(/^docker\.io\/library\//, '').replace(/^docker\.io\//, '');

export function externalImages(recipe) {
  const stages = new Set(['scratch']);
  const images = new Set();
  for (const line of recipe.split('\n')) {
    const match = line.match(/^FROM\s+(?:--platform=\S+\s+)?(\S+)(?:\s+AS\s+(\S+))?\s*$/i);
    if (!match) {
      if (/^FROM\s/i.test(line)) throw new Error(`Unsupported image declaration: ${line}`);
      continue;
    }
    const [, ref, stage] = match;
    if (/[${}]/.test(ref)) throw new Error(`Unresolved image declaration: ${ref}`);
    if (!stages.has(ref.toLowerCase())) images.add(normalize(ref));
    if (stage) stages.add(stage.toLowerCase());
  }
  if (!images.size) throw new Error('No external build images found');
  return [...images].sort();
}

export function resolvedImages(log) {
  return [...new Set([...log.matchAll(/\bFROM\s+(\S+@sha256:[a-f0-9]{64})\b/g)]
    .map(match => normalize(match[1])))].sort();
}

export function verifyImages(expected, actual, resolved) {
  const errors = [];
  if (JSON.stringify([...expected].sort()) !== JSON.stringify([...actual].sort())) {
    errors.push(`Expected ${expected.join(', ')}; agent generated ${actual.join(', ')}`);
  }
  for (const ref of actual) {
    if (!resolved.some(value => ref.includes('@') ? value === ref : value.startsWith(`${ref}@sha256:`))) {
      errors.push(`No BuildKit FROM digest evidence for ${ref}`);
    }
  }
  return errors;
}

export function coverageErrors(inventory, services, recipes) {
  return recipes.filter(path => {
    const name = path.split('/')[2];
    const service = services.find(service => service.name === name);
    return !service || !inventory[service.agent.name]?.images?.length;
  }).map(path => `No effective image coverage for ${path}`);
}

function check(inventory, services) {
  const errors = coverageErrors(inventory, services,
    discoverManifests(root).filter(isGeneratedRecipe).map(manifest => manifest.path));
  if (process.env.CODEFLY_BASE) {
    const changed = git('diff', '--name-only', process.env.CODEFLY_BASE, 'HEAD').trim().split('\n');
    for (const path of changed.filter(path => isGeneratedRecipe({ ecosystem: 'docker', path }))) {
      if (!changed.includes(bindingsPath)) {
        errors.push(`${path}: generated recipe changes require adoption through topology bindings; edits alone cannot change the build`);
      }
      const name = path.split('/')[2];
      const service = services.find(service => service.name === name);
      const expected = inventory[service?.agent.name]?.images ?? [];
      const proposed = externalImages(read(path));
      if (JSON.stringify(proposed) !== JSON.stringify([...expected].sort())) {
        errors.push(`${path}: proposed images differ from the effective image contract; update the owning agent and topology bindings`);
      }
    }
    if (changed.includes(inventoryPath) && !changed.includes(bindingsPath) &&
        git('ls-tree', '--name-only', process.env.CODEFLY_BASE, inventoryPath).trim()) {
      // Adding monitoring metadata does not require repinning an unchanged agent.
      const previous = JSON.parse(git('show', `${process.env.CODEFLY_BASE}:${inventoryPath}`));
      for (const [agent, config] of Object.entries(inventory)) {
        if (JSON.stringify(config.images) !== JSON.stringify(previous[agent]?.images)) {
          errors.push(`${agent}: image upgrades require an agent release adopted through topology bindings`);
        }
      }
    }
  }
  return errors;
}

function evidence(inventory, services) {
  const log = read('.codefly/ci/build.log');
  const resolved = resolvedImages(log);
  const selected = process.env.SELECTION_ALL === 'true'
    ? services : services.filter(service => (process.env.AFFECTED_SERVICES ?? '').split(/\s+/).includes(service.name));
  if (!selected.length) throw new Error('No services selected for build image evidence');
  const records = [];
  const errors = [];
  for (const service of selected) {
    const config = inventory[service.agent.name];
    if (!config) continue;
    const directory = `module/services/${service.name}/build-recipes/${service.agent.version}`;
    try {
      const manifest = JSON.parse(read(`${directory}/recipe.codefly.json`));
      if (manifest.schema !== 'codefly.dev/build-recipe/v2' || manifest.name !== service.agent.name || manifest.version !== service.agent.version) {
        throw new Error('Build recipe metadata does not match the pinned agent');
      }
      const recipes = manifest.recipes.map(recipe => ({ ...recipe, content: read(`${directory}/${recipe.dockerfile}`) }));
      const actual = [...new Set(recipes.flatMap(recipe => externalImages(recipe.content)))].sort();
      const failures = verifyImages(config.images, actual, resolved);
      errors.push(...failures.map(error => `${service.name}: ${error}`));
      records.push({ service: service.name, agent: service.agent, source: config.source, recipes,
        images: actual, resolved: resolved.filter(value => actual.some(ref => value === ref || value.startsWith(`${ref}@`))), errors: failures });
    } catch (error) {
      errors.push(`${service.name}: ${error.message}`);
      records.push({ service: service.name, agent: service.agent, errors: [error.message] });
    }
  }
  writeFileSync(resolve(root, '.codefly/ci/build-images.json'), `${JSON.stringify({ schemaVersion: 1, records }, null, 2)}\n`);
  console.log(JSON.stringify(records, null, 2));
  return errors;
}

function registryDigest(ref) {
  const manifest = JSON.parse(execFileSync('docker', ['buildx', 'imagetools', 'inspect', ref, '--format', '{{json .Manifest}}'], { encoding: 'utf8' }));
  if (!/^sha256:[a-f0-9]{64}$/.test(manifest.digest)) throw new Error(`Missing registry digest for ${ref}`);
  return manifest.digest;
}

export function monitor(inventory, digest = registryDigest) {
  const errors = [];
  const lines = ['# Effective build image updates', '', 'Update the owning agent source, publish a qualified release, then adopt it through topology bindings.', ''];
  for (const [agent, config] of Object.entries(inventory)) {
    lines.push(`## ${agent}`, '', config.source, '');
    for (const ref of config.images) {
      const [tag, pinned] = ref.split('@');
      const current = digest(tag);
      lines.push(`- Effective recipe: \`${ref}\`; current tag digest: \`${current}\``);
      if (pinned && current !== pinned) errors.push(`${agent}: ${tag} digest changed`);
      for (const watch of config.watch.filter(watch => watch.split(':')[0] === tag.split(':')[0])) {
        const latest = digest(watch);
        lines.push(`  - Watch \`${watch}@${latest}\``);
        if (latest !== (pinned ?? current)) errors.push(`${agent}: review ${watch} in ${config.source}`);
      }
    }
    lines.push('');
  }
  lines.push('## Review needed', '', ...errors.map(error => `- ${error}`));
  const summary = `${lines.join('\n')}\n`;
  console.log(summary);
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, summary);
  return errors;
}

if (resolve(process.argv[1] ?? '') === resolve(import.meta.filename)) {
  try {
    const inventory = JSON.parse(read(inventoryPath));
    const services = parseWorkflowYaml(read(bindingsPath)).services;
    const command = process.argv[2];
    const errors = command === 'check' ? check(inventory, services)
      : command === 'evidence' ? evidence(inventory, services)
      : command === 'monitor' ? monitor(inventory)
      : ['usage: build-images.mjs check|evidence|monitor'];
    if (errors.length) throw new Error(errors.join('\n'));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
