import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import {
	CatalogueEntryKind,
	CatalogueEntrySchema,
	CatalogueGapReason,
	CatalogueObservedVerdict,
} from "@/gen/saas/accounts/v1/platform_admin_pb";
import {
	SolutionRegistrationSchema,
	SolutionRegistrationStatus,
} from "@/gen/saas/accounts/v1/solution_registry_pb";
import {
	authorizationView,
	buildSizeView,
	CATALOGUE_STATES,
	catalogueGaps,
	catalogueStates,
	gapLabel,
	observedVerdictView,
	registrationView,
} from "./facts";

const notRecorded = (detail: string) => ({
	reason: CatalogueGapReason.NOT_RECORDED,
	detail,
});
const notObserved = {
	reason: CatalogueGapReason.NOT_OBSERVED,
	detail: "No cluster state.",
};

function entry(
	init: Parameters<typeof create<typeof CatalogueEntrySchema>>[1] = {},
) {
	return create(CatalogueEntrySchema, {
		kind: CatalogueEntryKind.SOLUTION,
		name: "example",
		...init,
	});
}

// The evidence an affirmative verdict rests on. A verdict reporting trouble
// needs none of it, which is why each piece is a separate argument here rather
// than baked into one fixture.
const approvedAt = (buildIncarnation = BigInt(3)) => ({
	authorizationValue: {
		case: "authorization" as const,
		value: {
			authorizedRevision: BigInt(4),
			inventoryDigest: "sha256:aaa",
			memberBinding: "member",
			buildIncarnation,
		},
	},
});

function observedWith(verdict: CatalogueObservedVerdict, observedAt?: Date) {
	return {
		observedExecutionValue: {
			case: "observedExecution" as const,
			value: {
				containerImageDigests: { "worker/worker": "sha256:bbb" },
				buildIncarnation: BigInt(3),
			},
		},
		...(observedAt
			? {
					observationFreshnessValue: {
						case: "observedAt" as const,
						value: timestampFromDate(observedAt),
					},
				}
			: {}),
		verdictValue: { case: "verdict" as const, value: verdict },
	};
}

describe("the state model", () => {
	it("is the registry's six states, in order", () => {
		expect([...CATALOGUE_STATES]).toEqual([
			"Desired",
			"Authorized",
			"Applied",
			"Observed",
			"Withdrawing",
			"Retired",
		]);
	});

	it("reads every state of an entry the host holds nothing for as gaps, never as values", () => {
		const e = entry({
			authorized: {
				authorizationValue: {
					case: "authorizationGap",
					value: notRecorded("No approval record."),
				},
			},
			applied: {
				appliedRevisionValue: {
					case: "appliedRevisionGap",
					value: notRecorded("No applied generation."),
				},
			},
			observed: { verdictValue: { case: "verdictGap", value: notObserved } },
			withdrawing: {
				withdrawalStateValue: {
					case: "withdrawalStateGap",
					value: notRecorded("No withdrawal record."),
				},
			},
			retired: {
				retirementStateValue: {
					case: "retirementStateGap",
					value: notObserved,
				},
			},
		});
		const states = catalogueStates(e);
		for (const state of [
			"Authorized",
			"Applied",
			"Withdrawing",
			"Retired",
		] as const) {
			for (const item of states[state])
				expect(item.view.kind, `${state} ${item.label}`).toBe("gap");
		}
		expect(observedVerdictView(e)).toEqual({
			kind: "gap",
			label: "Not observed",
			detail: "No cluster state.",
		});
		expect(gapLabel(CatalogueGapReason.NOT_REPORTED)).toBe("Not reported");
	});

	it("does not read an unset oneof as a value", () => {
		const states = catalogueStates(entry());
		for (const state of CATALOGUE_STATES) {
			for (const item of states[state]) {
				if (item.label === "Registration") continue;
				expect(item.view, `${state} ${item.label}`).toMatchObject({
					kind: "gap",
					label: "Unknown",
				});
			}
		}
	});

	it("renders a revision once the server sends one", () => {
		const e = entry({
			applied: {
				appliedRevisionValue: { case: "appliedRevision", value: BigInt(7) },
			},
			desired: {
				declaredReleaseValue: {
					case: "declaredRelease",
					value: { publisher: "example", name: "billing", version: "1.2.0" },
				},
			},
		});
		const states = catalogueStates(e);
		expect(states.Applied[0].view).toEqual({ kind: "value", text: "7" });
		expect(states.Desired[1].view).toEqual({
			kind: "value",
			text: "example/billing@1.2.0",
		});
	});
});

describe("authorization", () => {
	it("tells an approval, a known absence of one, and the host not knowing apart", () => {
		expect(
			authorizationView(
				entry({
					authorized: {
						authorizationValue: {
							case: "authorization",
							value: {
								authorizedRevision: BigInt(4),
								inventoryDigest: "sha256:aaa",
								memberBinding: "member",
								buildIncarnation: BigInt(3),
							},
						},
					},
				}),
			),
		).toMatchObject({
			kind: "value",
			text: "Revision 4",
			tone: "success",
			detail: "Approved inventory sha256:aaa, member member, incarnation 3.",
		});
		expect(
			authorizationView(
				entry({
					authorized: {
						authorizationValue: {
							case: "notAuthorized",
							value: { detail: "approval withdrawn" },
						},
					},
				}),
			),
		).toMatchObject({ kind: "value", text: "Not authorized" });
		expect(
			authorizationView(
				entry({
					authorized: {
						authorizationValue: {
							case: "authorizationGap",
							value: notRecorded("No approval record."),
						},
					},
				}),
			),
		).toEqual({
			kind: "gap",
			label: "Not recorded",
			detail: "No approval record.",
		});
	});
});

describe("observedVerdictView", () => {
	it("states running authorized when the server judged it so beside the evidence", () => {
		expect(
			observedVerdictView(
				entry({
					authorized: approvedAt(),
					observed: observedWith(
						CatalogueObservedVerdict.RUNNING_AUTHORIZED,
						new Date("2026-10-06T12:00:00Z"),
					),
				}),
			),
		).toMatchObject({
			kind: "value",
			text: "Running authorized",
			tone: "success",
		});
	});

	// A page that says "Running authorized" beside "Not observed" is worse than
	// one that says nothing, because a reader sees the green and stops. The server
	// will not send that combination; if one arrives, it is a defect and must read
	// as a fact this host cannot state.
	it.each([
		[
			"no approval to be authorized against",
			{
				observed: observedWith(
					CatalogueObservedVerdict.RUNNING_AUTHORIZED,
					new Date("2026-10-06T12:00:00Z"),
				),
			},
			"no approval",
		],
		[
			"an approval carrying no build incarnation",
			{
				authorized: approvedAt(BigInt(0)),
				observed: observedWith(
					CatalogueObservedVerdict.RUNNING_AUTHORIZED,
					new Date("2026-10-06T12:00:00Z"),
				),
			},
			"no build incarnation",
		],
		[
			"no time the observation was made",
			{
				authorized: approvedAt(),
				observed: observedWith(CatalogueObservedVerdict.RUNNING_AUTHORIZED),
			},
			"without the time the observation was made",
		],
		[
			"no observed execution",
			{
				authorized: approvedAt(),
				observed: {
					observationFreshnessValue: {
						case: "observedAt" as const,
						value: timestampFromDate(new Date("2026-10-06T12:00:00Z")),
					},
					verdictValue: {
						case: "verdict" as const,
						value: CatalogueObservedVerdict.RUNNING_AUTHORIZED,
					},
				},
			},
			"no observed execution",
		],
	])(
		"refuses to show an affirmative verdict with %s",
		(_name, init, missing) => {
			const view = observedVerdictView(entry(init));
			expect(view.kind).toBe("gap");
			expect(view.kind === "gap" && view.label).toBe("Inconsistent");
			expect(view.kind === "gap" && view.detail).toContain(missing);
		},
	);

	// The asymmetry: the evidence an affirmative verdict needs is not demanded of
	// one that reports trouble, which is still worth showing.
	it.each([
		["Differs from authorized", CatalogueObservedVerdict.RUNNING_DIFFERS],
		[
			"Running without authorization",
			CatalogueObservedVerdict.RUNNING_UNAUTHORIZED,
		],
	])("still reports %s without that evidence", (text, verdict) => {
		expect(
			observedVerdictView(entry({ observed: observedWith(verdict) })),
		).toMatchObject({ kind: "value", text, tone: "danger" });
	});

	it("flags an execution that differs from the approved one", () => {
		const view = observedVerdictView(
			entry({
				observed: observedWith(CatalogueObservedVerdict.RUNNING_DIFFERS),
			}),
		);
		expect(view).toMatchObject({
			text: "Differs from authorized",
			tone: "danger",
		});
		expect(view.kind === "value" && view.detail).toContain("sha256:bbb");
	});

	it("flags an execution nothing currently authorizes — the dangerous row", () => {
		expect(
			observedVerdictView(
				entry({
					observed: observedWith(CatalogueObservedVerdict.RUNNING_UNAUTHORIZED),
				}),
			),
		).toMatchObject({ text: "Running without authorization", tone: "danger" });
	});

	it("does not read an unspecified verdict as a value", () => {
		expect(
			observedVerdictView(
				entry({ observed: observedWith(CatalogueObservedVerdict.UNSPECIFIED) }),
			).kind,
		).toBe("gap");
	});
});

describe("registrationView", () => {
	it("tells a composed module, a solution known only by installations, and each status apart", () => {
		expect(
			registrationView(entry({ kind: CatalogueEntryKind.MODULE })),
		).toMatchObject({
			text: "Composed",
		});
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
	it("lists each unavailable fact once, named by its state, however many entries share it", () => {
		const shared = {
			buildSizeValue: {
				case: "buildSizeGap" as const,
				value: notRecorded("core#708"),
			},
			observed: {
				verdictValue: { case: "verdictGap" as const, value: notObserved },
			},
		};
		const gaps = catalogueGaps([
			entry(shared),
			entry({ ...shared, name: "other" }),
		]);
		expect(gaps.filter((g) => g.fact === "Build size")).toEqual([
			{ fact: "Build size", label: "Not recorded", detail: "core#708" },
		]);
		expect(gaps.filter((g) => g.fact === "Observed · Running")).toHaveLength(1);
		expect(buildSizeView(entry(shared)).kind).toBe("gap");
	});
});
