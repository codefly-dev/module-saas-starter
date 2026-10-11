import { readdirSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

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

// `logout` destructured out of the auth context. A control gets it exactly this
// way — there is no other accessor — so this is the whole population.
const TAKES_LOGOUT = /const\s*\{[^}]*\blogout\b[^}]*\}\s*=\s*useAuth\(\)/;

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
 * Source with comments removed.
 *
 * A structural sweep that reads raw text matches its own prose: the comment
 * beside the account-menu fix quotes the `onClick={logout}` it replaced, and
 * that quotation failed the rule the fix satisfies. Nothing here should depend
 * on how a defect is described, only on what the code does.
 */
function code(path: string): string {
	return readFileSync(path, "utf8")
		.replace(/\/\*[\s\S]*?\*\//g, "")
		.replace(/^[^\n]*?\/\/.*$/gm, (line) => (line.includes("://") ? line : ""));
}

function controls(): string[] {
	return sourceFiles().filter(
		(path) => !NOT_A_CONTROL.includes(path) && TAKES_LOGOUT.test(code(path)),
	);
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

	// A control must not sign out for itself, in ANY of the spellings. The list
	// is every way a control can reach `logout` without the journey deciding
	// where the person goes — including handing the function straight to a
	// component, which is how the account menu did it and which the previous
	// `(?:await|void)\s+logout\(\)` regex could not see.
	it.each(controls())("%s does not sign out for itself", (path: string) => {
		const source = code(path);
		const direct = [
			// `await logout()`, `void logout()`, `return logout()`.
			/(?:await|void|return)\s+logout\(\)/g,
			// `logout().then(...)`, `logout().catch(...)`.
			/\blogout\(\)\s*\./g,
			// The function handed to a prop or a callback, never called here:
			// `onClick={logout}`, `onSelect: logout`, `onPress={logout}`.
			/(?:=\{|:\s*)logout\s*[},)]/g,
		].flatMap((pattern) => source.match(pattern) ?? []);
		expect(direct).toEqual([]);
	});

	// The provider DEFINES `logout`, so it is the one file that calls it for
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
