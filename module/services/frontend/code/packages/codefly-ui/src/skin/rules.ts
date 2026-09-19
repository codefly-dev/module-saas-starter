import type {
	FrontendSkinRules,
	FrontendSlotRule,
	FrontendTypeSlotName,
} from "@codefly/saas-plugin-contract";

// The checker for layer 4. It reads rendered markup and never CSS, which is the
// whole point of keeping rules out of `appearance`: a rule is a constraint, and
// a constraint is checkable, but it is checkable against what a page RENDERS,
// not against what a stylesheet declares.
//
// It works off `data-slot`, the same attribute layer 3 points at, so a rule is
// stated about the same vocabulary a skin re-points. Run it over a story, a
// rendered route, or any DOM a test can produce.

export interface SkinRuleViolation {
	rule: "maxPerPage" | "headingOrder";
	/** The slot the rule is about, when it is about one. */
	slot?: FrontendTypeSlotName;
	message: string;
}

/**
 * Check one rendered tree against a skin's rules.
 *
 * `root` is anything with `querySelectorAll` — a container from a test render, a
 * document body. No rule reaches here from `appearance`, so a descriptor that
 * declares none produces no violations rather than an error.
 */
export function checkSkinRules(
	root: ParentNode,
	rules: FrontendSkinRules | undefined,
): SkinRuleViolation[] {
	if (!rules) return [];
	const violations: SkinRuleViolation[] = [];

	for (const [slot, rule] of Object.entries<FrontendSlotRule>(
		rules.slots ?? {},
	)) {
		if (rule?.maxPerPage === undefined) continue;
		const count = root.querySelectorAll(`[data-slot="${slot}"]`).length;
		if (count > rule.maxPerPage)
			violations.push({
				rule: "maxPerPage",
				slot: slot as FrontendTypeSlotName,
				message: `'${slot}' appears ${count} times; the skin allows ${rule.maxPerPage}`,
			});
	}

	if (rules.headingOrder === "no-skip") {
		const headings = [...root.querySelectorAll("h1, h2, h3, h4, h5, h6")];
		let previous = 0;
		for (const heading of headings) {
			const level = Number(heading.tagName.slice(1));
			// The first heading sets the starting level rather than being required
			// to be an h1: a fragment rendered on its own legitimately starts deeper.
			if (previous !== 0 && level > previous + 1)
				violations.push({
					rule: "headingOrder",
					message: `heading level jumps from h${previous} to h${level}`,
				});
			previous = level;
		}
	}

	return violations;
}

/** `checkSkinRules`, throwing one error that names every violation. */
export function assertSkinRules(
	root: ParentNode,
	rules: FrontendSkinRules | undefined,
): void {
	const violations = checkSkinRules(root, rules);
	if (violations.length === 0) return;
	throw new Error(
		`skin rules violated (${violations.length}):\n  ${violations
			.map((violation) => violation.message)
			.join("\n  ")}`,
	);
}
