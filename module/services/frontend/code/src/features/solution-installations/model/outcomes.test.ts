import { describe, expect, it } from "vitest";
import { InstallationStatus } from "@/gen/saas/accounts/v1/installations_pb";
import { installOutcome, retryOutcome, setUpOutcome } from "./outcomes";

const applied = (
	status = InstallationStatus.ACTIVE,
	accessProblems: string[] = [],
) => ({ status, accessProblems });

describe("what a finished act tells the person", () => {
	it("says an install is done only once it is active", () => {
		expect(installOutcome(applied())).toEqual({
			kind: "success",
			text: "Installed. Each role's team holds it now.",
		});
		expect(installOutcome(applied(InstallationStatus.PENDING))).toEqual({
			kind: "success",
			text: "Setup is pending. Each role's team receives it once setup is active.",
		});
	});

	it("offers recovery without exposing diagnostics when not every role's access was applied", () => {
		const problems = ["The team example-admin could not be created."];
		expect(
			installOutcome(applied(InstallationStatus.ACTIVE, problems)),
		).toEqual({
			kind: "warning",
			text: "Installed, but not every role's access was applied — grant the rest in Set up.",
		});
		expect(
			setUpOutcome("ledger", applied(InstallationStatus.ACTIVE, problems)),
		).toEqual({
			kind: "warning",
			text: "Set up, but not every role's access was applied — grant the rest in Set up.",
		});
		expect(
			retryOutcome("payroll", applied(InstallationStatus.PENDING, problems)),
		).toEqual({
			kind: "warning",
			text: "payroll: not every role's access was applied — grant the rest in Set up.",
		});
	});

	it("names the page whose access was set up, and the installation retried", () => {
		expect(setUpOutcome("ledger", applied()).text).toBe(
			"Access to ledger is set up. Each role's team holds it now.",
		);
		expect(
			retryOutcome("payroll", applied(InstallationStatus.PENDING)),
		).toEqual({ kind: "success", text: "payroll installation pending" });
	});
});
