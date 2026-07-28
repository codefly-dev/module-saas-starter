import { readFile, readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const scriptsDirectory = path.dirname(fileURLToPath(import.meta.url));
const serviceDirectory = path.resolve(scriptsDirectory, "..");
const sourceDirectory = path.join(serviceDirectory, "src");
const forbidden = [
  /(?:^|[/@])accounts(?:[/"]|$)/,
  /(?:^|[/@])frontend(?:[/"]|$)/,
  /(?:^|[/@])dashboard(?:[/"]|$)/,
  /(?:^|[/@])admin(?:[/"]|$)/,
  /(?:^|[/@])database(?:[/"]|$)/,
  /(?:^|[/@])store(?:[/"]|$)/,
  /(?:^|[/@])vault(?:[/"]|$)/,
  /@codefly\/sdk/,
  /server-only/,
];
const violations = [];

async function files(directory) {
  const output = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const entryPath = path.join(directory, entry.name);
    if (entry.isDirectory()) output.push(...(await files(entryPath)));
    else if (/\.(?:mjs|ts|tsx)$/.test(entry.name)) output.push(entryPath);
  }
  return output;
}

for (const file of await files(sourceDirectory)) {
  const source = await readFile(file, "utf8");
  for (const match of source.matchAll(
    /(?:import|export)\s+(?:[\s\S]*?\s+from\s+)?["']([^"']+)["']/g,
  )) {
    const specifier = match[1];
    if (forbidden.some((pattern) => pattern.test(specifier))) {
      violations.push(`${path.relative(serviceDirectory, file)} -> ${specifier}`);
    }
    if (specifier.startsWith("../") && path.resolve(path.dirname(file), specifier).startsWith(serviceDirectory) === false) {
      violations.push(`${path.relative(serviceDirectory, file)} escapes the service`);
    }
  }
}

if (violations.length > 0) {
  throw new Error(`Marketing dependency boundary violations:\n${violations.join("\n")}`);
}

process.stdout.write("Marketing dependency boundaries are valid.\n");
