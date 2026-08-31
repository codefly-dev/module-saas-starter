import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// The host owns the dynamic-editing mechanics and exposes them to a composing
// module through a boundary-safe channel; it must not name — or depend on — that
// module's conversational surface. This is the executable form of that boundary:
// the frontend source references none of the composing layer's vocabulary. The
// needles are assembled from fragments so this test file is not itself a match.
const FORBIDDEN = [
	["ch", "at"],
	["ag", "-ui"],
	["ro", "bin"],
].map((parts) => parts.join(""));

const SRC = path.resolve(process.cwd(), "src");

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
	it("references no composing-layer surface anywhere in the frontend source", () => {
		const offenders: string[] = [];
		for (const file of sourceFiles(SRC)) {
			const text = readFileSync(file, "utf8").toLowerCase();
			for (const needle of FORBIDDEN) {
				if (text.includes(needle)) {
					offenders.push(`${path.relative(SRC, file)} contains "${needle}"`);
				}
			}
		}
		expect(offenders).toEqual([]);
	});
});
