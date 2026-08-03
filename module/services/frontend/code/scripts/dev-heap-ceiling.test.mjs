import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

// The nextjs service agent starts the dev server with `npm run dev` under a
// POSIX shell, so the `dev` script is the only in-repo layer that governs the
// dev process heap. Under Turbopack + reactCompiler + transpiled plugin
// workspaces the dev compiler grows past Node's inherited ~2 GB old-space
// default and OOMs within minutes, taking the port with it. The ceiling must
// therefore be pinned to an explicit, above-default value here rather than
// inherited from the platform — and pinned by APPENDING to NODE_OPTIONS so any
// options the runtime already injected into the dev process survive.
const packageJson = JSON.parse(
	readFileSync(join(import.meta.dirname, "..", "package.json"), "utf8"),
);

const INHERITED_DEFAULT_MB = 2048;

const dev = packageJson.scripts?.dev ?? "";
// The dev server is the last `&&`-chained command; its inline env prefix is what
// reaches the `next dev` process.
const devSegment = dev.split("&&").at(-1).trim();

describe("frontend dev heap ceiling", () => {
	it("pins an explicit old-space ceiling above the inherited default", () => {
		const match = dev.match(/--max-old-space-size=(\d+)/);
		expect(
			match,
			`dev script must set --max-old-space-size: ${dev}`,
		).not.toBeNull();
		expect(Number(match[1])).toBeGreaterThan(INHERITED_DEFAULT_MB);
	});

	it("preserves inherited NODE_OPTIONS and appends the ceiling for the dev server", () => {
		expect(devSegment, `dev server command must be next dev: ${dev}`).toMatch(
			/\bnext dev\b/,
		);

		// Run the real env prefix from the dev script, substituting a probe for
		// `next dev` that reports the NODE_OPTIONS the dev process would see. A
		// replacing (non-appending) prefix would drop the inherited value below.
		const probe = devSegment.replace(
			/\bnext dev\b.*$/,
			"node -e \"process.stdout.write(process.env.NODE_OPTIONS ?? '')\"",
		);
		const effective = execFileSync("sh", ["-c", probe], {
			encoding: "utf8",
			env: { ...process.env, NODE_OPTIONS: "--enable-source-maps" },
		});

		expect(effective).toContain("--enable-source-maps");
		expect(effective).toMatch(/--max-old-space-size=\d+/);
	});
});
