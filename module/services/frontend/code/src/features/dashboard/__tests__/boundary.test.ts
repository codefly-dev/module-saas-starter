import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// The dynamic-dashboard channel exposes the host's editing mechanics to a
// composing module through a boundary-safe seam: neither the dashboard feature
// nor the solution-mount seam may name — or depend on — that module's
// conversational surface. This is the executable form of that boundary, scoped
// to exactly the two directories the channel spans. It is deliberately NOT
// repo-wide: unrelated services legitimately use some of these words, so a
// repo-wide ban would be both none of this channel's business and impossible to
// keep honest. Matching is whole-word so an unrelated identifier that merely
// contains a fragment does not trip it. The needles are assembled from fragments
// so this test file is not itself a match.
const FORBIDDEN = [
	["ch", "at"],
	["ag", "-ui"],
	["ro", "bin"],
].map((parts) => new RegExp(`\\b${parts.join("")}\\b`, "i"));

// Anchor to this file, not process.cwd(): the channel spans the dashboard
// feature (this test's grandparent) and the solutions mount seam, regardless of
// where the runner was invoked from.
const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOTS = [
	path.resolve(HERE, ".."), // src/features/dashboard
	path.resolve(HERE, "../../../solutions"), // src/solutions
];

function sourceFiles(dir: string): string[] {
	const files: string[] = [];
	for (const entry of readdirSync(dir)) {
		const full = path.join(dir, entry);
		if (statSync(full).isDirectory()) {
			files.push(...sourceFiles(full));
		} else if (/\.(ts|tsx)$/.test(full)) {
			files.push(full);
		}
	}
	return files;
}

describe("dynamic-dashboard channel boundary", () => {
	it("names no composing-layer surface in the dashboard feature or the mount seam", () => {
		const offenders: string[] = [];
		for (const root of ROOTS) {
			for (const file of sourceFiles(root)) {
				const text = readFileSync(file, "utf8");
				for (const pattern of FORBIDDEN) {
					if (pattern.test(text)) {
						offenders.push(`${path.relative(root, file)}: ${pattern.source}`);
					}
				}
			}
		}
		expect(offenders).toEqual([]);
	});
});
