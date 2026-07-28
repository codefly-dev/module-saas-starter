import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const scriptsDirectory = path.dirname(fileURLToPath(import.meta.url));
const generatedPath = path.resolve(
  scriptsDirectory,
  "..",
  "src",
  "generated",
  "public-site-config.ts",
);

export async function readGeneratedConfig() {
  const source = await readFile(generatedPath, "utf8");
  const prefix = "export const publicSiteConfig = Object.freeze(";
  const start = source.indexOf(prefix);
  const end = source.lastIndexOf(" as const);");
  if (start < 0 || end < 0) {
    throw new Error("generated public site configuration has an invalid shape");
  }
  return JSON.parse(source.slice(start + prefix.length, end));
}
