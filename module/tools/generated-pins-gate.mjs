#!/usr/bin/env node
// generated-pins-gate — a toolchain-free static gate over every service's
// checked-in generated Go, asserting it is the output of the plugin versions the
// service actually pins and carries the shape the generation pipeline produces.
//
// Codefly's `sync-drift` phase regenerates each service and fails on any byte
// difference against the tree. That gate is correct but reports late and opaquely:
// it names drifted files, not the reason, and it blocks the gate on every open PR
// before their tests run. Both halves of this gate encode a failure that already
// shipped:
//
//   - Stamp drift. #527 regenerated with ambient workstation plugins
//     (protoc-gen-go v1.36.12, protoc-gen-go-grpc v1.6.2) while
//     buf.gen.local.yaml pins v1.36.11 / v1.6.1, landing three files no pinned
//     run reproduces. It merged red and blocked the repo.
//   - Shape drift. The fix for that (#528) regenerated with `buf generate`
//     directly — which MODULE.md forbids in capitals — instead of
//     `codefly generate proto`. Raw plugin output emits one sorted import block
//     with reflect/sync/unsafe LAST; the pipeline runs goimports afterwards,
//     which splits off the stdlib group. The stamps were then correct and the
//     bytes still wrong, so the drift gate stayed red with an identical message.
//     Nothing else catches this: raw plugin output is valid gofmt, so no fmt or
//     lint step flags it.
//
// This reads the pins straight out of each `services/<svc>/buf.gen.local.yaml`,
// so it cannot fall out of step with them, and needs no protoc, buf, goimports or
// network. Zero-tolerance.
//
//   node tools/generated-pins-gate.mjs check   # fail on any stamp or shape drift
//
// The module root is the parent of tools/, so this works identically in
// canonical's `module/` and a consumer's `modules/<name>/`.

import { readdirSync, existsSync, statSync, readFileSync } from "node:fs";
import { join, dirname, resolve, relative } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const MODULE_ROOT = join(dirname(SCRIPT_PATH), "..");

// The stamp-bearing Go generators. Each names the go-run module suffix that
// identifies the plugin in a pinned `local:` invocation, the header line it
// writes, and which files under the plugin's out directory it owns. The
// grpc-gateway and connect-go plugins are deliberately absent: their output
// carries no version in the header, so there is nothing to compare.
const STAMPED_PLUGINS = [
  {
    plugin: "protoc-gen-go",
    // "// \tprotoc-gen-go v1.36.11"
    stamp: /^\/\/\s+protoc-gen-go v(\S+)$/m,
    owns: (name) => name.endsWith(".pb.go") && !name.endsWith("_grpc.pb.go") && !name.endsWith(".pb.gw.go"),
  },
  {
    plugin: "protoc-gen-go-grpc",
    // "// - protoc-gen-go-grpc v1.6.1"
    stamp: /^\/\/ - protoc-gen-go-grpc v(\S+)$/m,
    owns: (name) => name.endsWith("_grpc.pb.go"),
  },
];

// The stdlib imports protoc-gen-go and protoc-gen-go-grpc emit. goimports lifts
// these into the leading group; raw plugin output leaves them sorted in among the
// rest, which lands them last because "reflect" > "google.golang.org".
const STDLIB_IMPORT = /^\s*\w*\s*"(reflect|sync|unsafe|context)"$/;
// A third-party path: goimports keys on the dot in the first segment, so
// "google.golang.org/..." is third-party and "accounts/pkg/gen/..." is not.
const THIRD_PARTY_IMPORT = /^\s*\w*\s*"[^"/]*\.[^"/]*\//;

// Pinned plugin versions from one `buf.gen.local.yaml`, as
// { plugin -> { version, out } }. The file is a fixed-shape template this repo
// owns, so it is parsed line-wise rather than pulling in a YAML dependency —
// the same approach base-integrity.mjs takes to module.codefly.yaml.
export function parsePins(template) {
  const pins = new Map();
  const lines = template.split("\n");
  let inPlugins = false;
  let current = null;
  for (const line of lines) {
    if (/^plugins:\s*$/.test(line)) { inPlugins = true; continue; }
    if (!inPlugins) continue;
    if (/^\S/.test(line)) break; // dedent to col 0 → block ended

    const local = line.match(/^\s*-\s*local:\s*\[(.+)\]\s*$/);
    if (local) {
      // "go, run, google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11"
      const args = local[1].split(",").map((a) => a.trim());
      const pinned = args.find((a) => a.includes("@v"));
      current = null;
      if (pinned) {
        const [modulePath, version] = pinned.split("@");
        const plugin = modulePath.split("/").pop();
        current = { plugin, version };
      }
      continue;
    }
    const out = line.match(/^\s*out:\s*(\S+)\s*$/);
    if (out && current) {
      pins.set(current.plugin, { version: current.version, out: out[1] });
      current = null;
    }
  }
  return pins;
}

// Every generated file under `dir`, recursively, as paths relative to moduleRoot.
function generatedFiles(dir) {
  if (!existsSync(dir) || !statSync(dir).isDirectory()) return [];
  const found = [];
  for (const entry of readdirSync(dir).sort()) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) found.push(...generatedFiles(full));
    else found.push(full);
  }
  return found;
}

// goimports splits the stdlib group off with a blank line. Raw plugin output does
// not. Only files carrying both kinds of import can show the difference; a file
// with no third-party import (or no stdlib import) has one group either way and
// is legitimately unsplit.
function importShapeError(rel, source) {
  const block = source.match(/^import \(\n(.*?)^\)/ms);
  if (!block) return null;
  const lines = block[1].split("\n");
  const stdlib = lines.flatMap((l, i) => (STDLIB_IMPORT.test(l) ? [i] : []));
  const thirdParty = lines.flatMap((l, i) => (THIRD_PARTY_IMPORT.test(l) ? [i] : []));
  if (!stdlib.length || !thirdParty.length) return null;

  const lastStdlib = Math.max(...stdlib);
  const firstThirdParty = Math.min(...thirdParty);
  if (lastStdlib > firstThirdParty) {
    return `${rel}: stdlib imports sort after third-party — raw plugin output, not goimports-grouped`;
  }
  const separated = lines
    .slice(lastStdlib + 1, firstThirdParty)
    .some((l) => l.trim() === "");
  if (!separated) {
    return `${rel}: no blank line between the stdlib and third-party import groups — raw plugin output, not goimports-grouped`;
  }
  return null;
}

export function generatedPinsErrors(moduleRoot = MODULE_ROOT) {
  const servicesRoot = join(moduleRoot, "services");
  if (!existsSync(servicesRoot)) return [];

  const errors = [];
  for (const service of readdirSync(servicesRoot).sort()) {
    const template = join(servicesRoot, service, "buf.gen.local.yaml");
    if (!existsSync(template)) continue;
    const pins = parsePins(readFileSync(template, "utf8"));

    for (const { plugin, stamp, owns } of STAMPED_PLUGINS) {
      const pin = pins.get(plugin);
      if (!pin) continue;
      const outRoot = resolve(join(servicesRoot, service), pin.out);
      for (const file of generatedFiles(outRoot)) {
        if (!owns(file)) continue;
        const rel = relative(moduleRoot, file);
        const source = readFileSync(file, "utf8");
        // Both sides are compared bare: the stamp regex captures after the
        // literal "v", and the pin arrives from the go-run arg as "v1.36.11".
        const pinned = pin.version.replace(/^v/, "");
        const found = stamp.exec(source);
        if (!found) {
          errors.push(`${rel}: no ${plugin} version stamp; expected v${pinned}`);
          continue;
        }
        if (found[1] !== pinned) {
          errors.push(
            `${rel}: generated by ${plugin} v${found[1]} but ${service}/buf.gen.local.yaml pins v${pinned}`,
          );
        }
        const shape = importShapeError(rel, source);
        if (shape) errors.push(shape);
      }
    }
  }
  return [...new Set(errors)].sort();
}

function check() {
  const errors = generatedPinsErrors();
  if (errors.length) {
    console.error("generated-pins-gate: checked-in generated Go no pinned run reproduces:");
    errors.forEach((error) => console.error(`    ${error}`));
    console.error(
      `\nFAIL: ${errors.length} generated file(s) drifted from the pinned toolchain. Regenerate ` +
        "with `codefly generate proto --proto ./proto --output . --local --template buf.gen.local.yaml` " +
        "from the service directory (never `buf generate` directly — it skips the goimports pass), " +
        "then refresh module/tools/base-manifest.json.",
    );
    process.exit(1);
  }
  console.log("✓ every checked-in generated Go file matches its service's pinned plugin versions and shape.");
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) {
  const cmd = process.argv[2];
  if (cmd === "check") check();
  else {
    console.error("usage: generated-pins-gate.mjs check");
    process.exit(2);
  }
}
