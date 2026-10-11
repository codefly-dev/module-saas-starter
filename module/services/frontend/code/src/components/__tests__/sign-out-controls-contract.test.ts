import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// Where the sweep's own probes are written. Outside the tree, so a probe can
// never be mistaken for a control by the very sweep under test.
const sweepProbeDirectory = mkdtempSync(join(tmpdir(), "sign-out-sweep-"));

// Every user-facing sign-out control goes through the shared journey.
//
// This is a STRUCTURAL assertion, and says so: it reads source rather than
// behaviour, which proves only that no control calls `logout()` for itself —
// not that the journey is right. The journey's own behaviour is asserted in
// src/lib/__tests__/sign-out-journey.test.ts, and real controls are driven in
// consent-banner.test.tsx and account-menu-sign-out.test.tsx. This exists for
// the failure those cannot catch: the NEXT sign-out control, added months from
// now, quietly calling `logout()` and redirecting wherever it likes.
//
// It used to carry a FIXED LIST of five files, and that is how the account menu
// — the control most people actually use — went unfixed through two review
// rounds. The list named every control somebody had thought of; the one it did
// not name was invisible, and the regex it paired with the list matched only
// `await logout()` / `void logout()`, so the menu's `onClick={logout}` would not
// have been caught even if the file had been listed. A sweep whose membership is
// a judgement call tests the judgement, not the codebase.
//
// So the subject is DERIVED: every file that takes `logout` out of `useAuth()`
// is a sign-out control, found by reading the tree. Nothing has to remember to
// add one.

/** Every source file under src/, excluding tests and generated clients. */
function sourceFiles(): string[] {
	return readdirSync("src", { recursive: true, encoding: "utf8" })
		.map((entry) => `src/${entry}`)
		.filter((path) => path.endsWith(".ts") || path.endsWith(".tsx"))
		.filter((path) => !path.includes("__tests__"))
		.filter((path) => !path.endsWith(".test.ts") && !path.endsWith(".test.tsx"))
		.filter((path) => !path.startsWith("src/gen/"))
		.sort();
}

/**
 * The three files that are not controls and may call `logout` for themselves:
 * the auth provider, which DEFINES it; the journey, which IS the shared route;
 * and the logout page, which is where the journey SENDS an incomplete sign-out
 * and therefore owns the retry. The provider is held to its own rule below
 * rather than exempted outright.
 */
const NOT_A_CONTROL = [
	"src/lib/auth.tsx",
	"src/lib/sign-out-journey.ts",
	"src/features/auth/ui/logout-page.tsx",
];

/**
 * Source with comments and string literals removed.
 *
 * A structural sweep that reads raw text matches things that are not code. Two
 * false readings, both found by running it: the comment beside the account-menu
 * fix quotes the `onClick={logout}` it replaced, and the command palette's
 * search keywords are the literal string "sign out logout". Neither is a use of
 * anything. Nothing here should depend on how a defect is described or on what a
 * control calls itself.
 */
function code(path: string): string {
	return readFileSync(path, "utf8")
		.replace(/\/\*[\s\S]*?\*\//g, "")
		.replace(/^[^\n]*?\/\/.*$/gm, (line) => (line.includes("://") ? line : ""))
		.replace(/"(?:[^"\\\n]|\\.)*"/g, '""')
		.replace(/'(?:[^'\\\n]|\\.)*'/g, "''")
		.replace(/`(?:[^`\\]|\\.)*`/g, "``");
}

/**
 * The local name(s) a file binds the auth context's sign-out to.
 *
 * There are three spellings, and the first version of this sweep saw one:
 *
 *   const { logout } = useAuth()             -> "logout"
 *   const { logout: signOut } = useAuth()    -> "signOut"
 *   const auth = useAuth()                   -> "auth.logout"
 *
 * Deriving the NAME rather than matching the word is what makes the rule below
 * independent of spelling. A sweep keyed on the literal `logout` could not see
 * the renamed binding's uses at all, and could not see a member access either —
 * the round-4 review's probes are exactly those two.
 */
function signOutBindings(source: string): string[] {
	const names: string[] = [];
	for (const match of source.matchAll(/const\s*\{([^}]*)\}\s*=\s*useAuth\(\)/g)) {
		for (const field of match[1].split(",")) {
			const [key, alias] = field.split(":").map((part) => part.trim());
			if (key === "logout") names.push(alias || key);
		}
	}
	for (const match of source.matchAll(/const\s+([A-Za-z_$][\w$]*)\s*=\s*useAuth\(\)/g)) {
		// Taking the WHOLE context is not binding the sign-out. Plenty of files
		// read other fields off it and have no business with this rule; counting
		// them as controls would demand they mention the journey. The binding
		// exists only where the member is actually reached for.
		const member = `${match[1]}.logout`;
		if (source.includes(member)) names.push(member);
	}
	return names;
}

/** Any file that binds the context's sign-out, however it spells it. */
function controls(): string[] {
	return sourceFiles().filter(
		(path) => !NOT_A_CONTROL.includes(path) && signOutBindings(code(path)).length > 0,
	);
}

function escapeForRegExp(literal: string): string {
	return literal.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Every use of a bound sign-out that is NOT the argument to `signOutAndLeave`.
 *
 * The rule is stated as a WHITELIST of one, which is the only form that holds:
 * the previous version listed the spellings it refused, so `onClick={() =>
 * logout()}` beside a legitimate `signOutAndLeave(logout)` in the same file
 * passed both of its checks. A control has exactly one legitimate thing to do
 * with the sign-out — hand it to the journey — so anything else is a finding,
 * and nobody has to predict the next spelling.
 */
function straySignOutUses(path: string): string[] {
	const source = code(path);
	const stray: string[] = [];
	for (const name of signOutBindings(source)) {
		const remaining = source
			// the binding is not a use of itself
			.replace(/const\s*\{[^}]*\}\s*=\s*useAuth\(\)/g, "")
			.replace(/const\s+[A-Za-z_$][\w$]*\s*=\s*useAuth\(\)/g, "")
			// the one permitted use
			.split(`signOutAndLeave(${name}`)
			.join("signOutAndLeave(");
		const uses = remaining.match(
			new RegExp(`(^|[^\\w$.])${escapeForRegExp(name)}\\b`, "g"),
		);
		for (const use of uses ?? []) stray.push(`${name}: ${use.trim()}`);
	}
	return stray;
}

describe("sign-out controls", () => {
	// The population itself is asserted, because a derived sweep that found
	// NOTHING would pass every test below having checked no file at all — the
	// failure mode a fixed list at least could not have.
	it("finds the controls by reading the tree", () => {
		const found = controls();
		expect(found.length).toBeGreaterThanOrEqual(6);
		expect(found).toContain("src/components/admin-layout.tsx");
		expect(found).toContain("src/components/command-palette.tsx");
		expect(found).toContain("src/components/consent-banner.tsx");
	});

	it.each(controls())("%s routes through the shared journey", (path: string) => {
		expect(code(path)).toContain("signOutAndLeave");
	});

	it.each(controls())("%s does nothing else with the sign-out", (path: string) => {
		expect(straySignOutUses(path)).toEqual([]);
	});

	// The sweep is held to the spellings it was blind to, as source rather than
	// as a claim: each of these is a control that would have passed the previous
	// version, and each must be reported now.
	it.each([
		[
			"an arrow calling it beside a legitimate journey call",
			`const { logout } = useAuth();
			 onClick={() => void signOutAndLeave(logout)}
			 onClick={() => logout()}`,
		],
		[
			"a member access off the whole context",
			`const auth = useAuth();
			 onClick={() => { void auth.logout(); window.location.replace("/auth/login"); }}`,
		],
		[
			"a renamed binding",
			`const { logout: signOut } = useAuth();
			 onClick={async () => { await signOut(); }}`,
		],
	])("reports %s", (_name: string, source: string) => {
		const probe = `${sweepProbeDirectory}/probe.tsx`;
		writeFileSync(probe, source);
		try {
			expect(straySignOutUses(probe)).not.toEqual([]);
		} finally {
			rmSync(probe, { force: true });
		}
	});

	// The provider DEFINES the sign-out, so it is the one file that calls it for
	// itself — and its own callers must still read the outcome. A discarded
	// result there is the same defect one layer down: exitImpersonation tore the
	// credentials down and told the operator to sign in again without ever
	// looking at whether the sign-out completed.
	it("the auth provider reads every sign-out outcome it produces", () => {
		// Anchored to the start of a STATEMENT, so `const x = await logout();`
		// — the outcome being read — is not mistaken for the outcome being
		// thrown away.
		const discarded = code("src/lib/auth.tsx").match(/^\s*(?:await|void|return)\s+logout\(\)/gm);
		expect(discarded ?? []).toEqual([]);
	});
});
