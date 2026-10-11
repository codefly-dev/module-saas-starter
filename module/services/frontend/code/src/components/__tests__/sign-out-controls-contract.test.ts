import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// Every user-facing sign-out control goes through the shared journey.
//
// This is a STRUCTURAL assertion, and says so: it reads source rather than
// behaviour, which proves only that no control calls `logout()` for itself —
// not that the journey is right. The journey's own behaviour is asserted in
// src/lib/__tests__/sign-out-journey.test.ts, and two real controls are driven
// in consent-banner.test.tsx. This exists for the failure those cannot catch:
// the NEXT sign-out control, added months from now, quietly calling `logout()`
// and redirecting wherever it likes.
//
// That is exactly how this defect arrived. `logout()` was changed to report
// incompleteness and `/auth/logout` was taught to handle it, while the controls
// that already existed went on discarding the result — a per-control decision
// that nothing held together.
const controls = [
	"src/components/consent-banner.tsx",
	"src/components/command-palette.tsx",
	"src/features/onboarding/ui/onboarding-entry.tsx",
	"src/features/invitations/ui/invitation-acceptance.tsx",
	"src/features/organizations/service/lifecycle.ts",
];

describe("sign-out controls", () => {
	it.each(controls)("%s routes through the shared journey", (path) => {
		const source = readFileSync(path, "utf8");
		expect(source).toContain("signOutAndLeave");
	});

	// `await logout()` or `void logout()` as a control's own sign-out. The one
	// legitimate caller is the journey helper itself, and the logout PAGE, which
	// is the journey — neither is in the list above.
	it.each(controls)("%s does not call logout() for itself", (path) => {
		const source = readFileSync(path, "utf8");
		const direct = source.match(/(?:await|void)\s+logout\(\)/g) ?? [];
		expect(direct).toEqual([]);
	});
});
