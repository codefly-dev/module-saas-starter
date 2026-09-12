import { readFileSync, writeFileSync, appendFileSync, realpathSync, mkdirSync, existsSync } from 'node:fs';
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

export function materialImages(materials) {
  if (!Array.isArray(materials) || !materials.length) throw new Error('Missing BuildKit materials');
  return [...new Set(materials.flatMap(material => {
    if (!material.URI.startsWith('pkg:docker/')) return [];
    const uri = new URL(material.URI);
    const image = decodeURIComponent(uri.pathname.slice('docker/'.length));
    const separator = image.lastIndexOf('@');
    if (separator < 1 || !material.Digests?.length || material.Digests.some(digest => !/^sha256:[a-f0-9]{64}$/.test(digest))) {
      throw new Error(`Invalid Docker material: ${material.URI}`);
    }
    const ref = normalize(`${image.slice(0, separator)}:${image.slice(separator + 1)}`);
    return material.Digests.map(digest => `${ref}@${digest}`);
  }))].sort();
}

export function verifyImages(expected, resolved) {
  const matches = (ref, value) => ref.includes('@') ? ref === value : value.startsWith(`${ref}@sha256:`);
  const errors = expected.filter(ref => !resolved.some(value => matches(ref, value)))
    .map(ref => `Missing expected build material: ${ref}`);
  errors.push(...resolved.filter(value => !expected.some(ref => matches(ref, value)))
    .map(value => `Unexpected build material: ${value}`));
  return errors;
}

const docker = (...args) => execFileSync('docker', ['buildx', ...args], { encoding: 'utf8' });
const history = () => docker('history', 'ls', '--format', '{{json .}}').trim().split('\n').filter(Boolean).map(line => JSON.parse(line));

export function selectBuild(builds, context, dockerfile) {
  const matches = builds.filter(build => build.Context === context && build.Dockerfile === dockerfile);
  if (matches.length !== 1 || matches[0].Status !== 'completed') {
    throw new Error(`Expected one completed build for ${context}/${dockerfile}; found ${matches.length}`);
  }
  return matches[0];
}

export function coverageErrors(inventory, services, recipes) {
  const errors = services.filter(service => {
    const config = inventory[service.agent.name];
    return !config?.images?.length && config?.coverage !== 'codefly-vendor-audit';
  }).map(service => `No effective image coverage for ${service.name} (${service.agent.name})`);
  for (const path of recipes) {
    const service = services.find(service => service.name === path.split('/')[2]);
    if (!service || !inventory[service.agent.name]?.images?.length) errors.push(`No effective image coverage for ${path}`);
  }
  return errors;
}

function check(inventory, services) {
  const errors = coverageErrors(inventory, services,
    discoverManifests(root).filter(isGeneratedRecipe).map(manifest => manifest.path));
  if (process.env.CODEFLY_BASE) {
    const changed = git('diff', '--name-only', process.env.CODEFLY_BASE, 'HEAD').trim().split('\n');
    const proposals = git('diff', '--name-only', '--diff-filter=ACMRT', process.env.CODEFLY_BASE, 'HEAD').trim().split('\n');
    for (const path of proposals.filter(path => isGeneratedRecipe({ ecosystem: 'docker', path }))) {
      errors.push(`${path}: generated recipes are not upgrade inputs; adopt an agent through topology bindings and update the image contract`);
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
  const before = new Set(JSON.parse(read('.codefly/ci/build-history-before.json')));
  const builds = history().filter(record => !before.has(record.ref)).map(record => {
    const build = JSON.parse(docker('history', 'inspect', record.ref.split('/').at(-1), '--format', 'json'));
    if (existsSync(build.Context)) build.Context = realpathSync(build.Context);
    return build;
  });
  const selected = process.env.SELECTION_ALL === 'true'
    ? services : services.filter(service => (process.env.AFFECTED_SERVICES ?? '').split(/\s+/).includes(service.name));
  if (!selected.length) throw new Error('No services selected for build image evidence');
  const records = [];
  const errors = coverageErrors(inventory, services,
    discoverManifests(root).filter(isGeneratedRecipe).map(manifest => manifest.path));
  for (const service of selected) {
    const config = inventory[service.agent.name];
    if (!config?.images?.length) {
      records.push({ service: service.name, agent: service.agent, coverage: config?.coverage, errors: config ? [] : ['Missing coverage classification'] });
      continue;
    }
    const directory = `module/services/${service.name}/build-recipes/${service.agent.version}`;
    try {
      const manifest = JSON.parse(read(`${directory}/recipe.codefly.json`));
      if (manifest.schema !== 'codefly.dev/build-recipe/v2' || manifest.name !== service.agent.name || manifest.version !== service.agent.version) {
        throw new Error('Build recipe metadata does not match the pinned agent');
      }
      const recipes = manifest.recipes.map(recipe => ({ ...recipe, content: read(`${directory}/${recipe.dockerfile}`) }));
      const evidence = recipes.map(recipe => {
        const context = realpathSync(resolve(root, `module/services/${service.name}`, recipe.context ?? '.'));
        const build = selectBuild(builds, context, `builder/${recipe.dockerfile}`);
        const { Ref, Context, Dockerfile, Status, Platform, StartedAt, CompletedAt, Materials } = build;
        return { Ref, Context, Dockerfile, Status, Platform, StartedAt, CompletedAt, Materials };
      });
      const resolved = [...new Set(evidence.flatMap(build => materialImages(build.Materials)))].sort();
      const failures = verifyImages(config.images, resolved);
      errors.push(...failures.map(error => `${service.name}: ${error}`));
      records.push({ service: service.name, agent: service.agent, source: config.source, recipes, builds: evidence, resolved, errors: failures });
    } catch (error) {
      errors.push(`${service.name}: ${error.message}`);
      records.push({ service: service.name, agent: service.agent, errors: [error.message] });
    }
  }
  writeFileSync(resolve(root, '.codefly/ci/build-images.json'), `${JSON.stringify({ schemaVersion: 2, records }, null, 2)}\n`);
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
    if (config.coverage === 'codefly-vendor-audit') continue;
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
    if (command === 'snapshot') {
      mkdirSync(resolve(root, '.codefly/ci'), { recursive: true });
      writeFileSync(resolve(root, '.codefly/ci/build-history-before.json'), JSON.stringify(history().map(record => record.ref)));
    }
    const errors = command === 'snapshot' ? [] : command === 'check' ? check(inventory, services)
      : command === 'evidence' ? evidence(inventory, services)
      : command === 'monitor' ? monitor(inventory)
      : ['usage: build-images.mjs check|snapshot|evidence|monitor'];
    if (errors.length) throw new Error(errors.join('\n'));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
