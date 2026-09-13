#!/usr/bin/env node
// test-targets-gate — a static gate over every service's test-target catalog.
//
// The catalogs are this repository's application-side precursor to Core's
// effective-input contract: per-suite commands, runtime prerequisites, and the
// repository-relative inputs that invalidate a result. Two services publish one,
// both hand-maintained, and nothing held them to a common shape — so a typo'd
// input root, a budget on a target that starts nothing, or a bespoke key Core's
// schema cannot carry all read as valid metadata right up until a planner selects
// the wrong work from them.
//
// This asserts one schema across every catalog: the exact key sets (so a new
// bespoke field is a failure, not a silent dialect), roots that exist on disk,
// command tokens that are declared, and input identities in Core's vocabulary.
//
//   node tools/test-targets-gate.mjs check

import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const MODULE_ROOT = join(dirname(SCRIPT_PATH), "..");

const TOP_LEVEL = ["fallback", "path_base", "schema_version", "targets", "working_directory"];
const REQUIRED = ["command", "complete", "input_roots", "phase", "runtime_services", "setup_budget_seconds", "suite"];
const OPTIONAL = ["arguments", "external_inputs"];
const PHASES = ["lint", "compile", "test", "audit"];
// Core's EffectiveInputKind values that an application catalog can legitimately
// name before the agent resolves identities.
const KINDS = ["EXTERNAL", "TOOLCHAIN", "PLUGIN", "GENERATOR", "LOCKFILE"];
const TOKEN = /\$\{([a-z_]+)\}/g;

function catalogFileErrors(label, catalog, repoRoot) {
  const errors = [];
  const fail = (message) => errors.push(`${label}: ${message}`);
  const keys = Object.keys(catalog).sort();
  if (keys.join(",") !== TOP_LEVEL.join(",")) fail(`top-level keys ${keys.join(", ")} must be exactly ${TOP_LEVEL.join(", ")}`);
  if (catalog.schema_version !== 1) fail(`schema_version ${catalog.schema_version} is not 1`);
  if (catalog.path_base !== "repository") fail(`path_base ${catalog.path_base} must be "repository"`);
  if (catalog.fallback !== "whole-service-and-dependency-closure") fail(`fallback ${catalog.fallback} must be whole-service-and-dependency-closure`);
  const workingDirectory = join(repoRoot, catalog.working_directory ?? "");
  if (!existsSync(workingDirectory) || !statSync(workingDirectory).isDirectory()) fail(`working_directory ${catalog.working_directory} is not a directory`);
  if (!Array.isArray(catalog.targets) || catalog.targets.length === 0) return fail("targets must be a non-empty array"), errors;

  const suites = new Set();
  for (const target of catalog.targets) {
    const suite = target.suite;
    const where = `${label} target ${suite ?? "(unnamed)"}`;
    if (typeof suite !== "string" || suite === "") fail("a target has no suite name");
    if (suites.has(suite)) fail(`suite ${suite} is declared twice`);
    suites.add(suite);
    for (const key of REQUIRED) if (!Object.hasOwn(target, key)) errors.push(`${where}: missing ${key}`);
    for (const key of Object.keys(target)) {
      if (!REQUIRED.includes(key) && !OPTIONAL.includes(key)) {
        errors.push(`${where}: key ${key} is not in the shared schema (${[...REQUIRED, ...OPTIONAL].join(", ")})`);
      }
    }
    if (!PHASES.includes(target.phase)) errors.push(`${where}: phase ${target.phase} is not one of ${PHASES.join(", ")}`);
    if (!Array.isArray(target.command) || target.command.length === 0 || target.command.some((part) => typeof part !== "string" || part === "")) {
      errors.push(`${where}: command must be a non-empty array of non-empty strings`);
    } else {
      const declared = Object.keys(target.arguments ?? {});
      const used = new Set();
      for (const part of target.command) for (const [, name] of part.matchAll(TOKEN)) used.add(name);
      for (const name of used) if (!declared.includes(name)) errors.push(`${where}: command token \${${name}} has no arguments entry`);
      for (const name of declared) if (!used.has(name)) errors.push(`${where}: arguments entry ${name} is never used by the command`);
      // A path-shaped argument that does not exist is a command no consumer can run.
      for (const part of target.command) {
        if (!part.includes("/") || part.startsWith("-") || part.startsWith("./") || part.includes("${")) continue;
        if (!existsSync(join(workingDirectory, part))) errors.push(`${where}: command path ${part} does not exist`);
      }
    }
    if (!Array.isArray(target.runtime_services) || target.runtime_services.some((name) => typeof name !== "string")) {
      errors.push(`${where}: runtime_services must be an array of service names`);
    }
    if (!Number.isInteger(target.setup_budget_seconds) || target.setup_budget_seconds < 0) {
      errors.push(`${where}: setup_budget_seconds must be a non-negative integer`);
    }
    if (typeof target.complete !== "boolean") errors.push(`${where}: complete must be a boolean`);
    if (!Array.isArray(target.input_roots) || target.input_roots.length === 0) {
      errors.push(`${where}: input_roots must be a non-empty array`);
    } else {
      if (new Set(target.input_roots).size !== target.input_roots.length) errors.push(`${where}: input_roots contains duplicates`);
      for (const root of target.input_roots) {
        const path = join(repoRoot, root);
        if (root.endsWith("/")) {
          if (!existsSync(path) || !statSync(path).isDirectory()) errors.push(`${where}: input root ${root} is not a directory`);
        } else if (!existsSync(path) || statSync(path).isDirectory()) {
          errors.push(`${where}: input root ${root} is not a file; directory roots must end with /`);
        }
      }
    }
    for (const input of target.external_inputs ?? []) {
      if (!KINDS.includes(input.kind)) errors.push(`${where}: external input kind ${input.kind} is not one of ${KINDS.join(", ")}`);
      if (typeof input.name !== "string" || input.name === "") errors.push(`${where}: an external input has no name`);
      // An unresolved identity is what forces conservative selection; a plain
      // digest here would claim reuse eligibility the catalog has not earned.
      if (input.identity !== "unresolved" && !/^sha256:[0-9a-f]{64}$/.test(input.identity ?? "")) {
        errors.push(`${where}: external input ${input.name} identity must be "unresolved" or a sha256: digest`);
      }
    }
    // A target that starts nothing cannot spend dependency-setup time.
    const starts = (target.runtime_services?.length ?? 0) + (target.external_inputs?.length ?? 0);
    if (starts === 0 && target.setup_budget_seconds !== 0) {
      errors.push(`${where}: setup_budget_seconds ${target.setup_budget_seconds} but the target starts no dependency`);
    }
  }
  return errors;
}

export function catalogErrors(moduleRoot = MODULE_ROOT) {
  const repoRoot = join(moduleRoot, "..");
  const services = join(moduleRoot, "services");
  if (!existsSync(services)) return [];
  const errors = [];
  for (const service of readdirSync(services).sort()) {
    const path = join(services, service, "test-targets.json");
    if (!existsSync(path)) continue;
    const label = `services/${service}/test-targets.json`;
    let catalog;
    try {
      catalog = JSON.parse(readFileSync(path, "utf8"));
    } catch (error) {
      errors.push(`${label}: not valid JSON (${error.message})`);
      continue;
    }
    errors.push(...catalogFileErrors(label, catalog, repoRoot));
  }
  return errors;
}

function check() {
  const errors = catalogErrors();
  if (errors.length) {
    console.error("test-targets-gate: test-target catalogs that a planner cannot trust:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(`\nFAIL: ${errors.length} catalog error(s).`);
    process.exit(1);
  }
  console.log("✓ every test-target catalog matches the shared schema with inputs that exist.");
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  if (process.argv[2] === "check") check();
  else {
    console.error("usage: test-targets-gate.mjs check");
    process.exit(2);
  }
}
