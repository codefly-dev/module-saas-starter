/**
 * Server-only fixture loader. Reads the module-level YAML fixture files
 * so the frontend can display fixture users on the dev login page and
 * drive MSW mock data from the same source.
 *
 * The working directory when codefly starts the frontend service is
 * services/frontend/code/. Module fixtures live at ../../../fixtures/.
 */
import "server-only";

import { getCurrentFixture, isLocal } from "codefly";
import { existsSync, readFileSync } from "fs";
import yaml from "js-yaml";
import { resolve } from "path";
import { type FixtureFile, FixtureFileSchema } from "./types";

const FIXTURES_DIR =
	process.env.FIXTURES_DIR || resolve(process.cwd(), "../../../fixtures");

// Only alphanumeric, hyphens, underscores — prevents path traversal.
const SAFE_NAME = /^[a-zA-Z0-9_-]+$/;

export function loadFixture(name: string): FixtureFile | null {
	if (!isLocal() || !SAFE_NAME.test(name)) return null;
	const path = resolve(FIXTURES_DIR, `${name}.yaml`);
	if (!existsSync(path)) return null;
	const raw = readFileSync(path, "utf-8");
	const parsed = yaml.load(raw);
	return FixtureFileSchema.parse(parsed);
}

/**
 * The fixture this process may serve, or null. A fixture file carries every
 * fixture person's sign-in token, so a selected fixture is served only by a
 * local run: selecting one on a deployed host (by mistake or otherwise) yields
 * nothing here, exactly as if none were selected.
 */
export function activeFixtureName(): string | null {
	if (!isLocal()) return null;
	return getCurrentFixture() || null;
}

export function loadActiveFixture(): FixtureFile | null {
	const name = activeFixtureName();
	if (!name) return null;
	return loadFixture(name);
}
