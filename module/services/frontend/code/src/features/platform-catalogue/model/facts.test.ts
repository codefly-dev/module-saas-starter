import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
	CatalogueEntryKind,
	CatalogueEntrySchema,
	CatalogueGapReason,
	CatalogueRunningVerdict,
} from "@/gen/saas/accounts/v1/platform_admin_pb";
import {
	SolutionRegistrationSchema,
	SolutionRegistrationStatus,
} from "@/gen/saas/accounts/v1/solution_registry_pb";
import {
	buildSizeView,
	catalogueGaps,
	declaredReleaseView,
	gapLabel,
	generationView,
	registrationView,
	runningView,
} from "./facts";

const notObserved = {
	case: "runningGap" as const,
	value: {
		reason: CatalogueGapReason.NOT_OBSERVED,
		detail: "No cluster state.",
	},
};

function entry(
	init: Parameters<typeof create<typeof CatalogueEntrySchema>>[1] = {},
) {
	return create(CatalogueEntrySchema, {
		kind: CatalogueEntryKind.SOLUTION,
		name: "example",
		runningValue: notObserved,
		...init,
	});
}

describe("runningView", () => {
	const declared = { imageDigest: "sha256:aaa", buildIncarnation: BigInt(3) };

	it("states a match only when the server judged both executions equal", () => {
		const view = runningView(
			entry({
				runningValue: {
					case: "running",
					value: {
						declared,
						observed: declared,
						verdict: CatalogueRunningVerdict.MATCHES,
					},
				},
			}),
		);
		expect(view).toMatchObject({
			kind: "value",
			text: "Matches declared",
			tone: "success",
		});
	});

	it("flags a mismatch with both executions, since admission would refuse it", () => {
		const view = runningView(
			entry({
				runningValue: {
					case: "running",
					value: {
						declared,
						observed: {
							imageDigest: "sha256:bbb",
							buildIncarnation: BigInt(3),
						},
						verdict: CatalogueRunningVerdict.DIFFERS,
					},
				},
			}),
		);
		expect(view).toMatchObject({
			kind: "value",
			text: "Differs from declared",
			tone: "danger",
		});
		expect(view.kind === "value" && view.detail).toContain("sha256:aaa");
		expect(view.kind === "value" && view.detail).toContain("sha256:bbb");
	});

	it("says an unobserved deployment is not observed — never a match", () => {
		expect(runningView(entry())).toEqual({
			kind: "gap",
			label: "Not observed",
			detail: "No cluster state.",
		});
	});

	it("does not read an unset verdict or an unset oneof as a value", () => {
		expect(
			runningView(
				entry({
					runningValue: {
						case: "running",
						value: { declared, observed: declared },
					},
				}),
			).kind,
		).toBe("gap");
		expect(
			runningView(entry({ runningValue: { case: undefined } })),
		).toMatchObject({
			kind: "gap",
			label: "Unknown",
		});
	});
});

describe("presence-borne facts", () => {
	it("render the server's gap reason, never an empty value or zero", () => {
		const gap = {
			reason: CatalogueGapReason.NOT_RECORDED,
			detail: "No presence document.",
		};
		const e = entry({
			generationValue: { case: "generationGap", value: gap },
			buildSizeValue: { case: "buildSizeGap", value: gap },
			declaredReleaseValue: { case: "declaredReleaseGap", value: gap },
		});
		for (const view of [
			generationView(e),
			buildSizeView(e),
			declaredReleaseView(e),
		]) {
			expect(view).toEqual({
				kind: "gap",
				label: "Not recorded",
				detail: "No presence document.",
			});
		}
		expect(gapLabel(CatalogueGapReason.NOT_REPORTED)).toBe("Not reported");
	});

	it("render a value once the server sends one", () => {
		const e = entry({
			generationValue: { case: "generation", value: BigInt(7) },
			declaredReleaseValue: {
				case: "declaredRelease",
				value: { publisher: "example", name: "billing", version: "1.2.0" },
			},
		});
		expect(generationView(e)).toEqual({ kind: "value", text: "7" });
		expect(declaredReleaseView(e)).toEqual({
			kind: "value",
			text: "example/billing@1.2.0",
		});
	});
});

describe("registrationView", () => {
	it("tells a composed module, a solution known only by installations, and each status apart", () => {
		expect(
			registrationView(entry({ kind: CatalogueEntryKind.MODULE })),
		).toMatchObject({ text: "Composed" });
		expect(registrationView(entry())).toMatchObject({
			kind: "gap",
			label: "Not registered",
		});
		expect(
			registrationView(
				entry({
					registration: create(SolutionRegistrationSchema, {
						status: SolutionRegistrationStatus.INCOMPATIBLE,
					}),
				}),
			),
		).toMatchObject({ text: "Incompatible", tone: "danger" });
	});
});

describe("catalogueGaps", () => {
	it("lists each unavailable fact once, however many entries share it", () => {
		const gap = { reason: CatalogueGapReason.NOT_RECORDED, detail: "core#708" };
		const shared = {
			buildSizeValue: { case: "buildSizeGap" as const, value: gap },
		};
		const gaps = catalogueGaps([
			entry(shared),
			entry({ ...shared, name: "other" }),
		]);
		expect(gaps.filter((g) => g.fact === "Build size")).toEqual([
			{ fact: "Build size", label: "Not recorded", detail: "core#708" },
		]);
		expect(gaps.filter((g) => g.fact === "Running")).toHaveLength(1);
	});
});
