import { create } from "@bufbuild/protobuf";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ReviewedOfferSchema } from "@/gen/host/installation/v1/installation_pb";
import {
	AvailableSolutionSchema,
	ReviewedSolutionInstallSchema,
} from "@/gen/saas/accounts/v1/installations_pb";
import { InstallSolutionDialog } from "./install-solution-dialog";

const query = vi.hoisted(() => ({ isFetching: true }));
vi.mock("@tanstack/react-query", () => ({
	useQuery: () => ({ ...query, isError: false, data, refetch: vi.fn() }),
}));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ user: { id: "example-person" } }),
}));
vi.mock("@/lib/hooks/use-api-client", () => ({
	useInstallationService: () => ({}),
}));
const data = {
	consent: create(ReviewedSolutionInstallSchema, { installationRole: "none" }),
	offer: create(ReviewedOfferSchema, { installationRole: "none" }),
	modules: [],
};
const solution = create(AvailableSolutionSchema, {
	routeAlias: "Example Solution",
	targetId: "example-target",
});
function Dialog() {
	return (
		<InstallSolutionDialog
			orgId="example-org"
			solution={solution}
			onSubmit={vi.fn()}
			isPending={false}
			onClose={vi.fn()}
		/>
	);
}
const indicator = () =>
	screen.queryByRole("status", { name: "Preparing required access" });
const advance = async (ms: number) => {
	await act(async () => {
		await vi.advanceTimersByTimeAsync(ms);
	});
};
beforeEach(() => {
	query.isFetching = true;
	vi.useFakeTimers();
});
afterEach(() => {
	cleanup();
	vi.useRealTimers();
});

describe("installation review uses the shared loading timing", () => {
	it("never shows an indicator for preparation completed before 200ms", async () => {
		const { rerender } = render(<Dialog />);
		expect(indicator()).toBeNull();
		expect(
			(screen.getByRole("button", { name: "Install" }) as HTMLButtonElement)
				.disabled,
		).toBe(true);
		await advance(199);
		query.isFetching = false;
		rerender(<Dialog />);
		await advance(1000);
		expect(indicator()).toBeNull();
	});
	it("keeps an appeared indicator for 300ms after its 200ms delay", async () => {
		const { rerender } = render(<Dialog />);
		await advance(199);
		expect(indicator()).toBeNull();
		await advance(1);
		expect(indicator()).not.toBeNull();
		await advance(1);
		query.isFetching = false;
		rerender(<Dialog />);
		await advance(298);
		expect(indicator()).not.toBeNull();
		await advance(1);
		expect(indicator()).toBeNull();
		fireEvent.click(screen.getByRole("checkbox"));
		expect(
			(screen.getByRole("button", { name: "Install" }) as HTMLButtonElement)
				.disabled,
		).toBe(false);
		query.isFetching = true;
		rerender(<Dialog />);
		expect(
			(screen.getByRole("button", { name: "Install" }) as HTMLButtonElement)
				.disabled,
		).toBe(true);
	});
});
