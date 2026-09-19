// Layer 4: constraints a design system has STATED, carried as data and checked
// at build time. "Display 01 appears once, at the top of a page, never in
// product" is not a render token, but it is a perfectly good lint rule.
//
// This layer is deliberately NOT part of `appearance`. Layers 1 to 3 are
// consumed at render time and every value they carry reaches CSS, so they go
// through the injection gate. A rule reaches a checker and never a stylesheet,
// so it needs its own schema — and putting it inside `appearance` would mean
// widening the thing whose narrowness is the security property.
//
// Only rules something actually enforces are declared. A field that is accepted
// and then ignored is worse than an absent one: it reads as a promise. Usage
// rules that need a notion of "surface" are therefore not here yet — there is no
// surface vocabulary to check them against.

import {
	FRONTEND_TYPE_SLOT_NAMES,
	type FrontendTypeSlotName,
} from "./typography.js";

/** How a heading level may follow the one before it. */
export type FrontendHeadingOrderRule = "no-skip" | "any";

export interface FrontendSlotRule {
	/**
	 * How many times this slot may appear in one rendered page. A display role
	 * used twice is the most common way a system's hierarchy is broken.
	 */
	maxPerPage?: number;
}

export interface FrontendSkinRules {
	slots?: Readonly<Partial<Record<FrontendTypeSlotName, FrontendSlotRule>>>;
	/** `no-skip`: a heading may not jump a level (h2 straight to h4). */
	headingOrder?: FrontendHeadingOrderRule;
}

export const DEFAULT_FRONTEND_SKIN_RULES: FrontendSkinRules = Object.freeze({
	slots: Object.freeze({}),
	headingOrder: "any",
});

const SKIN_RULES_FIELDS = ["slots", "headingOrder"] as const;
const SLOT_RULE_FIELDS = ["maxPerPage"] as const;

function assertRules(condition: unknown, message: string): asserts condition {
	if (!condition) throw new Error(`Invalid frontend skin rules: ${message}`);
}

function assertObject(
	value: unknown,
	context: string,
): asserts value is Record<string, unknown> {
	assertRules(
		value !== null && typeof value === "object" && !Array.isArray(value),
		`${context} must be an object`,
	);
}

function assertKnownKeys(
	value: Record<string, unknown>,
	allowed: readonly string[],
	context: string,
): void {
	const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
	assertRules(
		unknown.length === 0,
		`${context} has unknown field '${unknown[0]}'`,
	);
}

/** Fail-closed, like every other half of the contract. */
export function resolveSkinRules(rules: unknown): FrontendSkinRules {
	if (rules === undefined) return DEFAULT_FRONTEND_SKIN_RULES;
	assertObject(rules, "rules");
	assertKnownKeys(rules, SKIN_RULES_FIELDS, "rules");

	const headingOrder = rules.headingOrder ?? "any";
	assertRules(
		headingOrder === "no-skip" || headingOrder === "any",
		`rules headingOrder '${String(headingOrder)}' is unsupported`,
	);

	const slots: Record<string, FrontendSlotRule> = {};
	if (rules.slots !== undefined) {
		assertObject(rules.slots, "rules slots");
		assertKnownKeys(rules.slots, FRONTEND_TYPE_SLOT_NAMES, "rules slots");
		for (const [slot, rule] of Object.entries(rules.slots)) {
			const context = `rules slot '${slot}'`;
			assertObject(rule, context);
			assertKnownKeys(rule, SLOT_RULE_FIELDS, context);
			const max = (rule as FrontendSlotRule).maxPerPage;
			if (max !== undefined)
				assertRules(
					typeof max === "number" && Number.isInteger(max) && max >= 0,
					`${context} maxPerPage must be a whole number of appearances`,
				);
			slots[slot] = Object.freeze({ maxPerPage: max });
		}
	}

	return Object.freeze({
		slots: Object.freeze(slots) as FrontendSkinRules["slots"],
		headingOrder,
	});
}
