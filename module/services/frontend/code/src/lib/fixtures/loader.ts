/**
 * Server-only fixture loader. Reads the module-level YAML fixture files
 * so the frontend can display fixture users on the dev login page and
 * drive MSW mock data from the same source.
 *
 * The working directory when codefly starts the frontend service is
 * services/frontend/code/. Module fixtures live at ../../../fixtures/.
 */
import "server-only";

import { readFileSync, existsSync } from "fs";
import { resolve } from "path";
import yaml from "js-yaml";
import { FixtureFileSchema, type FixtureFile } from "./types";

const FIXTURES_DIR =
  process.env.FIXTURES_DIR || resolve(process.cwd(), "../../../fixtures");

// Only alphanumeric, hyphens, underscores — prevents path traversal.
const SAFE_NAME = /^[a-zA-Z0-9_-]+$/;

export function loadFixture(name: string): FixtureFile | null {
  if (!SAFE_NAME.test(name)) return null;
  const path = resolve(FIXTURES_DIR, `${name}.yaml`);
  if (!existsSync(path)) return null;
  const raw = readFileSync(path, "utf-8");
  const parsed = yaml.load(raw);
  return FixtureFileSchema.parse(parsed);
}

export function activeFixtureName(): string | null {
  return process.env.CODEFLY__FIXTURE || null;
}

export function loadActiveFixture(): FixtureFile | null {
  const name = activeFixtureName();
  if (!name) return null;
  return loadFixture(name);
}
