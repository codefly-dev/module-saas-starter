import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

// The nextjs service agent starts the dev server with `npm run dev`, so the
// `dev` script is the only in-repo layer that governs the dev process heap.
// Under Turbopack + reactCompiler + transpiled plugin workspaces the dev
// compiler grows past Node's inherited ~2 GB old-space default and OOMs within
// minutes, taking the port with it. The ceiling must therefore be pinned to an
// explicit, above-default value here rather than inherited from the platform.
const packageJson = JSON.parse(
	readFileSync(new URL("../package.json", import.meta.url), "utf8"),
);

const INHERITED_DEFAULT_MB = 2048;

describe("frontend dev heap ceiling", () => {
	const dev = packageJson.scripts?.dev ?? "";

	it("pins an explicit old-space ceiling on the dev server", () => {
		const match = dev.match(/--max-old-space-size=(\d+)/);
		expect(match, `dev script must set --max-old-space-size: ${dev}`).not.toBeNull();
		expect(Number(match[1])).toBeGreaterThan(INHERITED_DEFAULT_MB);
	});

	it("applies the ceiling to the next dev invocation", () => {
		expect(dev).toMatch(/NODE_OPTIONS=--max-old-space-size=\d+ next dev/);
	});
});
