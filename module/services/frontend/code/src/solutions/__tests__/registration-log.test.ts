import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
	observeRegistrationBeat,
	observeRegistrationRemoved,
} from "@/solutions/registration-log";

const registered = (revision = 7, status = "active") =>
	({ ok: true, revision, status }) as const;
const refused = (httpStatus: number, reason: string) =>
	({ ok: false, httpStatus, reason }) as const;

describe("registration heartbeat log", () => {
	let info: ReturnType<typeof vi.spyOn>;
	let warn: ReturnType<typeof vi.spyOn>;

	beforeEach(() => {
		(globalThis as Record<string, unknown>).__solutionRegistrationLog =
			undefined;
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
		for (let beat = 0; beat < 10; beat++) {
			observeRegistrationBeat("example", refused(503, "registry unavailable"));
		}
		expect(warn).toHaveBeenCalledTimes(1);
		expect(
			observeRegistrationBeat("example", refused(409, "registry conflict")),
		).toContain("was refused 503 registry unavailable after 11 beats");
		expect(warn).toHaveBeenCalledTimes(2);
		expect(observeRegistrationBeat("example", registered(8))).toContain(
			"registered again (revision 8, active) — recovered from 409 registry conflict after 1 beat",
		);
		expect(info).toHaveBeenCalledTimes(2);
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
});
