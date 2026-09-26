import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
	observeAuthorityReachable,
	observeAuthorityRefusal,
	observeRegistrationBeat,
	observeRegistrationRemoved,
} from "@/solutions/registration-log";

const registered = (revision = 7, status = "active") =>
	({ ok: true, revision, status }) as const;
const refused = (
	httpStatus: number,
	reason: "invalid_manifest" | "incompatible_runtime" | "registry unavailable",
	detail?: string,
) => ({ ok: false, httpStatus, reason, detail }) as const;

describe("registration heartbeat log", () => {
	let info: ReturnType<typeof vi.spyOn>;
	let warn: ReturnType<typeof vi.spyOn>;

	beforeEach(() => {
		const scope = globalThis as Record<string, unknown>;
		scope.__solutionRegistrationLog = undefined;
		scope.__solutionRegistrationAuthority = undefined;
		info = vi.spyOn(console, "info").mockImplementation(() => {});
		warn = vi.spyOn(console, "warn").mockImplementation(() => {});
	});

	afterEach(() => {
		vi.restoreAllMocks();
	});

	it("logs a registration once and never the beats that renew it", () => {
		expect(observeRegistrationBeat("example", registered())).toContain(
			'"example" registered (revision 7, active)',
		);
		for (let beat = 0; beat < 100; beat++) {
			expect(observeRegistrationBeat("example", registered())).toBeUndefined();
		}
		expect(info).toHaveBeenCalledTimes(1);
		expect(warn).not.toHaveBeenCalled();
	});

	it("logs a new revision with the beats the previous one held", () => {
		observeRegistrationBeat("example", registered(7));
		observeRegistrationBeat("example", registered(7));
		observeRegistrationBeat("example", registered(7));
		expect(observeRegistrationBeat("example", registered(9))).toContain(
			"now at revision 9 (active; was revision 7, active, held after 3 beats)",
		);
		expect(info).toHaveBeenCalledTimes(2);
	});

	it("logs a refusal once, a different refusal again, and the recovery", () => {
		observeRegistrationBeat("example", registered(7));
		expect(
			observeRegistrationBeat("example", refused(503, "registry unavailable")),
		).toContain(
			'"example" refused 503 registry unavailable — was registered at revision 7 after 1 beat',
		);
		for (let beat = 0; beat < 8; beat++) {
			observeRegistrationBeat("example", refused(503, "registry unavailable"));
		}
		expect(warn).toHaveBeenCalledTimes(1);
		expect(
			observeRegistrationBeat("example", refused(409, "incompatible_runtime")),
		).toContain("was refused 503 registry unavailable after 9 beats");
		expect(warn).toHaveBeenCalledTimes(2);
		expect(observeRegistrationBeat("example", registered(8))).toContain(
			"registered again (revision 8, active) — recovered from 409 incompatible_runtime after 1 beat",
		);
		expect(info).toHaveBeenCalledTimes(2);
	});

	// A refusal that prints once and never again is a log that goes quiet while
	// the registration is still broken. It says so again at 10, 100, 1000 …
	it("says a standing refusal is still standing, at milestones", () => {
		for (let beat = 0; beat < 100; beat++) {
			observeRegistrationBeat("example", refused(503, "registry unavailable"));
		}
		expect(warn).toHaveBeenCalledTimes(3);
		expect(String(warn.mock.calls[1]?.[0])).toContain(
			"is still refused 503 registry unavailable after 10 beats",
		);
		expect(String(warn.mock.calls[2]?.[0])).toContain(
			"is still refused 503 registry unavailable after 100 beats",
		);
	});

	// The reason is the identity of the refusal; the detail is not. A registrant
	// that varies one field per beat must not get a line per beat.
	it("does not re-log a refusal whose detail changes but whose reason does not", () => {
		observeRegistrationBeat(
			"example",
			refused(409, "incompatible_runtime", "host React 19 does not satisfy 17"),
		);
		expect(warn).toHaveBeenCalledTimes(1);
		expect(String(warn.mock.calls[0]?.[0])).toContain(
			"host React 19 does not satisfy 17",
		);
		for (let beat = 0; beat < 8; beat++) {
			expect(
				observeRegistrationBeat(
					"example",
					refused(409, "incompatible_runtime", `attempt ${beat}`),
				),
			).toBeUndefined();
		}
		expect(warn).toHaveBeenCalledTimes(1);
	});

	// A log record's boundary is this module's to hold: a newline in a value
	// taken from the request body writes a second line that reads like a real
	// one.
	it("escapes control characters in every value it interpolates", () => {
		observeRegistrationBeat(
			"example",
			refused(
				403,
				"invalid_manifest",
				'manifest names "x\nsolution registration: "audit" registered (revision 999, active)"',
			),
		);
		const line = String(warn.mock.calls[0]?.[0]);
		expect(line).not.toContain("\n");
		expect(line).toContain("\\x0a");
	});

	it("bounds the detail it will print", () => {
		observeRegistrationBeat(
			"example",
			refused(409, "incompatible_runtime", "x".repeat(5000)),
		);
		expect(String(warn.mock.calls[0]?.[0]).length).toBeLessThan(500);
	});

	it("keeps each registrant's state apart", () => {
		observeRegistrationBeat("example-a", registered(1));
		observeRegistrationBeat("example-b", registered(1));
		observeRegistrationBeat("example-a", registered(1));
		observeRegistrationBeat("example-b", registered(1));
		expect(info).toHaveBeenCalledTimes(2);
	});

	it("logs a removal, and the next beat as a fresh registration", () => {
		observeRegistrationBeat("example", registered(7));
		observeRegistrationBeat("example", registered(7));
		expect(observeRegistrationRemoved("example", 8)).toContain(
			'"example" removed its registration (revision 8), held after 2 beats',
		);
		expect(observeRegistrationBeat("example", registered(9))).toContain(
			'"example" registered (revision 9, active)',
		);
	});

	it("holds a bounded number of registrants", () => {
		for (let index = 0; index < 1000; index++) {
			observeRegistrationBeat(`example-${index}`, registered(1));
		}
		const states = (globalThis as Record<string, unknown>)
			.__solutionRegistrationLog as Map<string, unknown>;
		expect(states.size).toBeLessThanOrEqual(256);
		expect(states.has("example-999")).toBe(true);
		expect(states.has("example-0")).toBe(false);
	});

	// A registration that beats steadily without changing is the most recently
	// seen of all. Held at the position of its last CHANGE, it is the first
	// entry a churning neighbour evicts — and its next beat then logs a fresh
	// registration that never happened.
	it("does not evict the registrant that is beating most steadily", () => {
		observeRegistrationBeat("steady", registered(1));
		for (let index = 0; index < 1000; index++) {
			observeRegistrationBeat("steady", registered(1));
			observeRegistrationBeat(`churn-${index}`, registered(index + 2));
		}
		const states = (globalThis as Record<string, unknown>)
			.__solutionRegistrationLog as Map<string, unknown>;
		expect(states.size).toBeLessThanOrEqual(256);
		expect(states.has("steady")).toBe(true);
		expect(observeRegistrationBeat("steady", registered(1))).toBeUndefined();
	});
});

// A beat whose credential did not verify belongs to no registrant. Held under a
// stand-in one it could never recover — nothing unverified ever succeeds — so
// the key set coming back was never reported, and a refused credential and an
// unreachable key set at the same time evicted each other's state and printed
// on every beat.
describe("registration credential-check log", () => {
	let info: ReturnType<typeof vi.spyOn>;
	let warn: ReturnType<typeof vi.spyOn>;

	beforeEach(() => {
		const scope = globalThis as Record<string, unknown>;
		scope.__solutionRegistrationLog = undefined;
		scope.__solutionRegistrationAuthority = undefined;
		info = vi.spyOn(console, "info").mockImplementation(() => {});
		warn = vi.spyOn(console, "warn").mockImplementation(() => {});
	});

	afterEach(() => {
		vi.restoreAllMocks();
	});

	it("reports the key set coming back, which no beat can say for itself", () => {
		expect(observeAuthorityRefusal("unreachable")).toContain(
			"the registration key set is unreachable",
		);
		for (let beat = 0; beat < 8; beat++) {
			expect(observeAuthorityRefusal("unreachable")).toBeUndefined();
		}
		expect(warn).toHaveBeenCalledTimes(1);
		expect(observeAuthorityReachable()).toContain(
			"the registration key set is reachable again; it answered 503 for 9 beats",
		);
		expect(info).toHaveBeenCalledTimes(1);
		// Nothing left open: a later verified beat is not a second recovery.
		expect(observeAuthorityReachable()).toBeUndefined();
		expect(info).toHaveBeenCalledTimes(1);
	});

	it("keeps an unreachable key set and a refused credential apart", () => {
		observeAuthorityRefusal("unreachable");
		observeAuthorityRefusal("refused");
		expect(warn).toHaveBeenCalledTimes(2);
		for (let beat = 0; beat < 8; beat++) {
			expect(observeAuthorityRefusal("unreachable")).toBeUndefined();
			expect(observeAuthorityRefusal("refused")).toBeUndefined();
		}
		expect(warn).toHaveBeenCalledTimes(2);
	});

	// Someone else's beat verifying says nothing about the caller whose
	// credential this host rejected.
	it("does not clear a refused credential when another beat verifies", () => {
		observeAuthorityRefusal("refused");
		expect(observeAuthorityReachable()).toBeUndefined();
		for (let beat = 0; beat < 8; beat++) {
			observeAuthorityRefusal("refused");
		}
		expect(warn).toHaveBeenCalledTimes(1);
		expect(observeAuthorityRefusal("refused")).toContain("after 10 beats");
	});

	it("says a standing key set outage is still standing, at milestones", () => {
		for (let beat = 0; beat < 100; beat++) {
			observeAuthorityRefusal("unreachable");
		}
		expect(warn).toHaveBeenCalledTimes(3);
		expect(String(warn.mock.calls[2]?.[0])).toContain("after 100 beats");
	});
});
