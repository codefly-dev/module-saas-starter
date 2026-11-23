/**
 * Server-only fixture loader. Reads the module-level YAML fixture files
 * so the frontend can display fixture users on the dev login page and
 * drive MSW mock data from the same source.
 *
 * The working directory when codefly starts the frontend service is
 * services/frontend/code/. Module fixtures live at ../../../fixtures/.
 */
import "server-only";

import { getCurrentFixture } from "codefly";
import { existsSync, readFileSync } from "fs";
import yaml from "js-yaml";
import { resolve } from "path";
import { type FixtureFile, FixtureFileSchema } from "./types";

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

// The environment name Codefly injects into every process it starts. The
// JavaScript SDK exposes the selected fixture (getCurrentFixture) but has no
// accessor for the environment yet, so this one carrier is read here, in one
// place. STOPGAP: replace with the SDK accessor when sdk-js grows one (the Go
// SDK's is codefly.IsLocal, which the accounts service uses for the same gate).
const ENVIRONMENT_CARRIER = "CODEFLY__ENVIRONMENT";
const LOCAL_ENVIRONMENT = "local";

/**
 * True only inside a local Codefly run. Fails closed: an absent, empty or
 * unrecognised environment is not local. Only the server-side carrier counts —
 * a NEXT_PUBLIC_ copy is build-time browser configuration, not the identity of
 * the running process. The comparison matches the Go SDK's (case-insensitive,
 * `-` and `_` equivalent).
 */
export function isLocalEnvironment(
	env: Record<string, string | undefined> = process.env,
): boolean {
	const name = env[ENVIRONMENT_CARRIER];
	if (!name) return false;
	return name.toLowerCase().replaceAll("-", "_") === LOCAL_ENVIRONMENT;
}

/**
 * The fixture this process may serve, or null. A fixture file carries every
 * fixture person's sign-in token, so a selected fixture is served only by a
 * local run: selecting one on a deployed host (by mistake or otherwise) yields
 * nothing here, exactly as if none were selected.
 */
export function activeFixtureName(): string | null {
	if (!isLocalEnvironment()) return null;
	return getCurrentFixture() || null;
}

export function loadActiveFixture(): FixtureFile | null {
	const name = activeFixtureName();
	if (!name) return null;
	return loadFixture(name);
}
