import {
	HOST_REACT_VERSION,
	HOST_SHARED_VERSIONS,
	SOLUTION_HOST_CONTRACT_MAJOR,
	SOLUTION_MANIFEST_SCHEMA_MAJOR,
} from "./host-runtime";
import type { SolutionManifest } from "./registry";

/**
 * Runtime compatibility, enforced at registration.
 *
 * A solution remote executes in the host origin against the host's own React,
 * kit, and SDK instances. Declaring what it needs and then loading it anyway is
 * not a compatibility check: the failure surfaces as a broken page — or a
 * half-mounted one — long after the browser has already run the code. So every
 * declared requirement is checked HERE, before the registration is activated,
 * and a mismatch is refused with a reason an operator can act on.
 */

/**
 * The semver range grammar a solution may declare. Deliberately a subset:
 * `||`-separated alternatives, each a space-separated conjunction of `^`, `~`,
 * `>=`, `>`, `<=`, `<`, `=`, a bare version, or `*`. Anything outside it is
 * REFUSED rather than approximated — a range the host cannot evaluate is not a
 * requirement it can honour.
 */
const COMPARATOR =
	/^(\^|~|>=|<=|>|<|=)?(\d+|[xX*])(?:\.(\d+|[xX*]))?(?:\.(\d+|[xX*]))?$/;

type Version = [number, number, number];

function parseVersion(value: string): Version | null {
	// Build metadata and prerelease tags are dropped: the host publishes release
	// versions, and a remote's range is about the release line it needs.
	const core = value.trim().split(/[-+]/)[0];
	const parts = core.split(".");
	if (parts.length === 0 || parts.length > 3) {
		return null;
	}
	const numbers = parts.map((part) => Number.parseInt(part, 10));
	if (numbers.some((n) => !Number.isInteger(n) || n < 0)) {
		return null;
	}
	return [numbers[0], numbers[1] ?? 0, numbers[2] ?? 0];
}

function compare(a: Version, b: Version): number {
	for (let i = 0; i < 3; i++) {
		if (a[i] !== b[i]) {
			return a[i] < b[i] ? -1 : 1;
		}
	}
	return 0;
}

/**
 * Upper bound (exclusive) of a partially specified version — `19`, `19.x`,
 * `19.2` — which stands for every release that fills in the parts it omitted.
 */
function partialCeiling([major, minor]: Version, specified: number): Version {
	return specified < 2 ? [major + 1, 0, 0] : [major, minor + 1, 0];
}

/**
 * Upper bound (exclusive) of a `^` comparator: changes that do not modify the
 * left-most NON-ZERO element. Which element that is depends on how many parts
 * were given, so `^0` (≥0.0.0 <1.0.0) and `^0.0` (≥0.0.0 <0.1.0) differ.
 */
function caretCeiling(bound: Version, specified: number): Version {
	const [major, minor, patch] = bound;
	if (specified < 2 || major > 0) {
		return [major + 1, 0, 0];
	}
	if (minor > 0 || specified < 3) {
		return [0, minor + 1, 0];
	}
	return [0, 0, patch + 1];
}

/**
 * Upper bound (exclusive) of a `~`: the last part given may move. `~19` allows
 * all of 19.x, while `~19.2` allows only 19.2.x.
 */
function tildeCeiling([major, minor]: Version, specified: number): Version {
	return specified < 2 ? [major + 1, 0, 0] : [major, minor + 1, 0];
}

function satisfiesComparator(
	version: Version,
	comparator: string,
): boolean | null {
	const match = COMPARATOR.exec(comparator);
	if (!match) {
		return null;
	}
	const [, operator, majorText, minorText, patchText] = match;
	const wildcard = (part: string | undefined) =>
		part === undefined || part === "x" || part === "X" || part === "*";
	// An x/*/absent part means "unspecified from here on", so everything after it
	// is unspecified too: `19.x.2` names no more than `19.x`.
	if (wildcard(majorText)) {
		return true;
	}
	const specified = wildcard(minorText) ? 1 : wildcard(patchText) ? 2 : 3;
	const bound: Version = [
		Number.parseInt(majorText, 10),
		specified > 1 ? Number.parseInt(minorText as string, 10) : 0,
		specified > 2 ? Number.parseInt(patchText as string, 10) : 0,
	];
	switch (operator) {
		case ">=":
			return compare(version, bound) >= 0;
		case ">":
			return compare(version, bound) > 0;
		case "<=":
			return compare(version, bound) <= 0;
		case "<":
			return compare(version, bound) < 0;
		case "^":
			return (
				compare(version, bound) >= 0 &&
				compare(version, caretCeiling(bound, specified)) < 0
			);
		case "~":
			return (
				compare(version, bound) >= 0 &&
				compare(version, tildeCeiling(bound, specified)) < 0
			);
		default:
			// A fully specified bare version is exact; a partial one stands for the
			// releases that fill in what it omitted (`19` is every 19.x.y).
			if (specified === 3) {
				return compare(version, bound) === 0;
			}
			return (
				compare(version, bound) >= 0 &&
				compare(version, partialCeiling(bound, specified)) < 0
			);
	}
}

/**
 * Whether `version` satisfies `range`, or null when the range is outside the
 * supported grammar. Null is a refusal, never a pass.
 */
export function satisfiesRange(version: string, range: string): boolean | null {
	const parsed = parseVersion(version);
	if (!parsed || range.trim() === "") {
		return null;
	}
	let anyAlternative = false;
	for (const alternative of range.split("||")) {
		const comparators = alternative
			.replace(/(\^|~|>=|<=|>|<|=)\s+/g, "$1")
			.trim()
			.split(/\s+/)
			.filter(Boolean);
		if (comparators.length === 0) {
			return null;
		}
		let all = true;
		for (const comparator of comparators) {
			const result = satisfiesComparator(parsed, comparator);
			if (result === null) {
				return null;
			}
			all &&= result;
		}
		anyAlternative ||= all;
	}
	return anyAlternative;
}

/**
 * An exposed module key names one entry in the remote's Module-Federation
 * `exposes` map, which the host loads as `<id>/<name>`. Requiring the `./Name`
 * shape keeps a registrant from smuggling a path or a traversal into that key.
 */
const EXPOSED_MODULE = /^\.\/[A-Za-z0-9][A-Za-z0-9._-]*$/;

export interface CompatibilityVerdict {
	compatible: boolean;
	/** Operator-facing reasons, one per failed requirement. Empty when compatible. */
	reasons: string[];
}

/**
 * Check a parsed manifest's declared runtime requirements against what this host
 * actually publishes. Undeclared requirements default to the current major
 * rather than to "anything": a solution that says nothing is asserting it was
 * built against today's contract, which is the only claim the host can act on.
 */
export function checkRuntimeCompatibility(
	manifest: SolutionManifest,
): CompatibilityVerdict {
	const reasons: string[] = [];

	if (manifest.schemaVersion !== SOLUTION_MANIFEST_SCHEMA_MAJOR) {
		reasons.push(
			`manifest schema major ${manifest.schemaVersion} is not supported (host serves ${SOLUTION_MANIFEST_SCHEMA_MAJOR})`,
		);
	}
	if (manifest.frontend.hostContract !== SOLUTION_HOST_CONTRACT_MAJOR) {
		reasons.push(
			`host contract major ${manifest.frontend.hostContract} is not supported (host serves ${SOLUTION_HOST_CONTRACT_MAJOR})`,
		);
	}
	if (!EXPOSED_MODULE.test(manifest.frontend.exposedModule)) {
		reasons.push(
			`exposed module ${JSON.stringify(manifest.frontend.exposedModule)} is not a "./Name" key`,
		);
	}

	const { reactRange } = manifest.frontend;
	if (reactRange !== undefined) {
		const satisfied = satisfiesRange(HOST_REACT_VERSION, reactRange);
		if (satisfied === null) {
			reasons.push(
				`react range ${JSON.stringify(reactRange)} is not a supported range`,
			);
		} else if (!satisfied) {
			reasons.push(
				`host React ${HOST_REACT_VERSION} does not satisfy required ${reactRange}`,
			);
		}
	}

	for (const [pkg, range] of Object.entries(manifest.frontend.shared ?? {})) {
		const hosted = Object.hasOwn(HOST_SHARED_VERSIONS, pkg)
			? HOST_SHARED_VERSIONS[pkg]
			: undefined;
		if (hosted === undefined) {
			// The host shares a fixed, sealed set. A remote requiring anything else
			// would bundle its own copy and break the seal, so the requirement is
			// refused rather than silently unmet.
			reasons.push(`shared package ${pkg} is not published by this host`);
			continue;
		}
		const satisfied = satisfiesRange(hosted, range);
		if (satisfied === null) {
			reasons.push(
				`${pkg} range ${JSON.stringify(range)} is not a supported range`,
			);
		} else if (!satisfied) {
			reasons.push(`host ${pkg} ${hosted} does not satisfy required ${range}`);
		}
	}

	return { compatible: reasons.length === 0, reasons };
}
