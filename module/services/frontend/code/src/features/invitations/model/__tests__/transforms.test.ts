import { describe, expect, it } from "vitest";
import { formatInvitationStatus } from "../transforms";

describe("formatInvitationStatus", () => {
	it("returns Pending with default variant for status 1", () => {
		expect(formatInvitationStatus(1)).toEqual({
			label: "Pending",
			variant: "default",
		});
	});

	it("returns Accepted with secondary variant for status 2", () => {
		expect(formatInvitationStatus(2)).toEqual({
			label: "Accepted",
			variant: "secondary",
		});
	});

	it("returns Revoked with destructive variant for status 3", () => {
		expect(formatInvitationStatus(3)).toEqual({
			label: "Revoked",
			variant: "destructive",
		});
	});

	it("returns Expired with outline variant for status 4", () => {
		expect(formatInvitationStatus(4)).toEqual({
			label: "Expired",
			variant: "outline",
		});
	});

	it("returns Unknown with outline variant for status 0 (unspecified)", () => {
		expect(formatInvitationStatus(0)).toEqual({
			label: "Unknown",
			variant: "outline",
		});
	});

	it("returns Unknown with outline variant for unrecognized status", () => {
		expect(formatInvitationStatus(99)).toEqual({
			label: "Unknown",
			variant: "outline",
		});
	});
});
