#!/usr/bin/env node
import { getCurrentFixture } from "codefly";

// Build-time preflight for the terms gate.
//
// NEXT_PUBLIC_* vars are inlined at BUILD time (see next.config.mjs). A
// production build that forgets to supply operator legal content ships a terms
// gate that can never be satisfied — every authenticated user is stranded on an
// un-acceptable Terms banner. That failure is invisible until runtime, so we
// surface it here, at the point the values are baked in.
//
// SAFE-BY-DEFAULT counterpart: legal-config.ts only serves dev placeholders when
// NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER opts in, so a real deploy never silently
// ships placeholder terms. This guard catches the other side — a real deploy
// missing real content.
//
// WARN-FIRST: this prints a warning and exits 0. Flip `ENFORCE` to make missing
// content fail the build once every deploy path reliably supplies it.
const ENFORCE = false;

const LEGAL_KEYS = [
	"NEXT_PUBLIC_LEGAL_ENTITY_NAME",
	"NEXT_PUBLIC_LEGAL_CONTACT_EMAIL",
	"NEXT_PUBLIC_LEGAL_TERMS_CONTENT",
	"NEXT_PUBLIC_LEGAL_PRIVACY_CONTENT",
];

// A fixture run (`--fixture <name>` or the E2E runner) marks the dev boundary;
// a real deploy never runs under one.
function fixtureActive() {
	return getCurrentFixture().trim() !== "";
}

function devPlaceholdersEnabled() {
	const flag =
		process.env.NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER?.trim().toLowerCase();
	// next.config.mjs derives this from the fixture boundary when unset.
	if (flag === "true" || flag === "1") return true;
	if (flag === "false" || flag === "0" || flag === "") return false;
	return fixtureActive();
}

// A dev/E2E build gets placeholders and is expected to lack operator content.
if (devPlaceholdersEnabled()) {
	process.exit(0);
}

const missing = LEGAL_KEYS.filter((key) => !(process.env[key] ?? "").trim());
if (missing.length === 0) {
	process.exit(0);
}

const message =
	`legal content incomplete for a production build — the Terms gate will be ` +
	`unacceptable until these NEXT_PUBLIC_* values are set at build time:\n` +
	missing.map((key) => `  - ${key}`).join("\n") +
	`\nSet them before building for a real deployment, or set ` +
	`NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER=true for a local/dev build.`;

if (ENFORCE) {
	console.error(`\n[check-public-legal-env] ERROR: ${message}\n`);
	process.exit(1);
}
console.warn(`\n[check-public-legal-env] WARNING: ${message}\n`);
process.exit(0);
