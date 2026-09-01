import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, relative, resolve, sep } from "node:path";
import { describe, expect, it } from "vitest";

// The default-deny guard for ARCHITECTURE.md: keep the kit turtles-down. A
// presentational tier may compose only the tier BELOW it, and every kit
// component stays pure (no host context, no network) so host and remote render
// identical output from one shared package instance. This is the structural
// enforcement of invariants 1 and 3; the narrower "don't paste a primitive's
// class string" check and the concrete dashboard→layout de-duplication are #402.

// The presentational stack, low rank → high, as a machine-readable copy of the
// diagram in ARCHITECTURE.md. `charts` is still nested in `dashboard/` today
// (extracted by #403); `chat`/`table`/`form` are composite tiers that sit at the
// same rank as `dashboard` — they compose the atoms below, never each other.
// `plugin-host` and the root entry are deliberately absent: they are the
// host-facing surface, not part of the presentational turtle stack.
const TIER_RANK: Record<string, number> = {
	skin: 0,
	layout: 1,
	charts: 2,
	dashboard: 3,
	chat: 3,
	table: 3,
	form: 3,
};

// A cross-tier import is a layering violation when it does not flow strictly
// downward: a presentational tier importing a tier above it, or a composite
// importing a sibling composite. Same-tier imports and imports of non-tier code
// (react, `@codefly/*` peers) are fine.
function layeringViolation(sourceDir: string, targetDir: string): boolean {
	const source = TIER_RANK[sourceDir];
	const target = TIER_RANK[targetDir];
	if (source === undefined) return false; // not a presentational tier
	if (target === undefined) return false; // not a kit tier
	if (sourceDir === targetDir) return false; // intra-tier composition is fine
	return target >= source;
}

// Every module specifier in an import/export ... from "…" or a bare side-effect
// `import "…"`.
function specifiers(source: string): string[] {
	const out: string[] = [];
	for (const match of source.matchAll(
		/(?:import|export)\b[^;]*?\bfrom\s*["']([^"']+)["']/g,
	)) {
		out.push(match[1]);
	}
	for (const match of source.matchAll(/import\s*["']([^"']+)["']/g)) {
		out.push(match[1]);
	}
	return out;
}

// Resolve a specifier to the kit tier directory it points at, or null if it
// leaves the kit (react, a `@codefly/*` peer, a relative hop out of `src`).
function targetTierDir(
	spec: string,
	fileDir: string,
	srcDir: string,
): string | null {
	if (spec.startsWith("@codefly-dev/ui/")) return spec.split("/")[2] ?? null;
	if (spec.startsWith(".")) {
		const rel = relative(srcDir, resolve(fileDir, spec));
		if (rel.startsWith("..")) return null;
		return rel.split(sep)[0];
	}
	return null;
}

// Locate this package's `src` under either Vitest project's cwd: the kit's own
// node config runs from the package root, the host's happy-dom `pure` project
// runs from the frontend `code` root (which globs `packages/**`).
function codeflyUiSrcDir(): string {
	for (const candidate of ["src", "packages/codefly-ui/src"]) {
		const abs = resolve(process.cwd(), candidate);
		if (existsSync(join(abs, "index.ts"))) return abs;
	}
	throw new Error("could not locate the @codefly-dev/ui src directory");
}

function sourceFiles(dir: string): string[] {
	const out: string[] = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		if (entry.name === "__tests__") continue;
		const full = join(dir, entry.name);
		if (entry.isDirectory()) out.push(...sourceFiles(full));
		else if (/\.tsx?$/.test(entry.name)) out.push(full);
	}
	return out;
}

const srcDir = codeflyUiSrcDir();
const files = sourceFiles(srcDir);

// A sanity floor: if this ever finds nothing the guards below are vacuous and
// would pass a broken tree silently.
it("scans the kit source tree", () => {
	expect(files.length).toBeGreaterThan(0);
});

describe("layeringViolation detector", () => {
	it("flags an upward import", () => {
		expect(layeringViolation("layout", "dashboard")).toBe(true);
	});
	it("flags a sibling-composite import", () => {
		expect(layeringViolation("dashboard", "chat")).toBe(true);
	});
	it("allows a downward import", () => {
		expect(layeringViolation("dashboard", "layout")).toBe(false);
		expect(layeringViolation("charts", "skin")).toBe(false);
	});
	it("allows intra-tier and non-tier imports", () => {
		expect(layeringViolation("dashboard", "dashboard")).toBe(false);
		expect(layeringViolation("layout", "react")).toBe(false);
		expect(layeringViolation("plugin-host", "layout")).toBe(false);
	});
});

describe("kit stays turtles-down (compose, never upward)", () => {
	for (const file of files) {
		const sourceDir = relative(srcDir, file).split(sep)[0];
		if (TIER_RANK[sourceDir] === undefined) continue;
		it(`${relative(srcDir, file)} imports only tiers below it`, () => {
			const source = readFileSync(file, "utf8");
			for (const spec of specifiers(source)) {
				const target = targetTierDir(spec, join(file, ".."), srcDir);
				if (target === null) continue;
				expect(
					layeringViolation(sourceDir, target),
					`${sourceDir} tier imports "${spec}" (tier ${target}) — a higher or sibling tier; compose the tier below instead`,
				).toBe(false);
			}
		});
	}
});

describe("kit is pure, data-in (no host context, no network)", () => {
	for (const file of files) {
		it(`${relative(srcDir, file)} fetches nothing and imports no host code`, () => {
			const source = readFileSync(file, "utf8");
			expect(
				source,
				`${file} imports host app code via the "@/" alias`,
			).not.toMatch(/(?:from|import)\s*["']@\//);
			expect(source, `${file} performs network I/O`).not.toMatch(
				/\bfetch\s*\(|\bXMLHttpRequest\b|\bnew\s+WebSocket\b|\bnavigator\s*\.\s*sendBeacon\b/,
			);
		});
	}
});
