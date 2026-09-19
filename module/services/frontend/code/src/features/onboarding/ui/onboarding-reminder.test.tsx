import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The reminder only appears once required setup is done and optional setup is
// not, which is a state the real controller only reaches against a backend.
// Mocked here so the closable-ness can be tested on its own; the persistence
// behind it is covered by browser-reminder-store.test.ts.
const authState = vi.hoisted(() => ({
	isAuthenticated: true,
	organizationId: "org-1",
	switchOrganization: vi.fn(),
}));

const onboardingState = vi.hoisted(() => ({
	controller: {} as never,
	model: {
		phase: "ready",
		progress: { requiredComplete: true, checklistComplete: false },
	} as never,
}));

vi.mock("@/lib/auth", () => ({ useAuth: () => authState }));
vi.mock("next/navigation", () => ({
	usePathname: () => "/",
	useRouter: () => ({ replace: vi.fn() }),
}));
vi.mock("../react/use-onboarding-controller", () => ({
	useOnboardingController: () => onboardingState,
}));

function Wrapper({ children }: { children: ReactNode }) {
	return (
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			{children}
		</QueryClientProvider>
	);
}

// The gate holds ONE reminder store for the module, which caches what it read
// so `useSyncExternalStore` is not re-parsing storage every render. That is
// right for a page and wrong for a suite: one test's dismissal would otherwise
// still be dismissed in the next. Each test therefore imports a fresh graph.
async function renderGate() {
	const { OnboardingGate } = await import("./onboarding-gate");
	return render(
		<Wrapper>
			<OnboardingGate>
				<div>dashboard content</div>
			</OnboardingGate>
		</Wrapper>,
	);
}

const reminder = () =>
	screen.queryByRole("complementary", { name: "Finish workspace setup" });

beforeEach(() => {
	window.localStorage.clear();
	vi.resetModules();
});

afterEach(() => {
	cleanup();
	window.localStorage.clear();
	vi.resetModules();
});

describe("the optional-setup reminder", () => {
	// A green run must mean "the reminder was there and went away", never "the
	// mock did not take and there was nothing to dismiss".
	it("appears while optional setup is outstanding", async () => {
		await renderGate();
		expect(reminder()).not.toBeNull();
		expect(
			screen.getByRole("button", { name: "Resume checklist" }),
		).toBeTruthy();
	});

	it("can be dismissed, leaving the product untouched", async () => {
		await renderGate();
		expect(reminder()).not.toBeNull();
		fireEvent.click(
			screen.getByRole("button", {
				name: "Dismiss workspace setup reminder",
			}),
		);
		expect(reminder()).toBeNull();
		expect(screen.getByText("dashboard content")).toBeTruthy();
	});

	it("stays dismissed across a re-render", async () => {
		const first = await renderGate();
		fireEvent.click(
			screen.getByRole("button", {
				name: "Dismiss workspace setup reminder",
			}),
		);
		first.unmount();
		await renderGate();
		expect(reminder()).toBeNull();
	});

	it("names the dismiss control for assistive technology", async () => {
		await renderGate();
		const dismiss = screen.getByRole("button", {
			name: "Dismiss workspace setup reminder",
		});
		expect(dismiss.getAttribute("aria-label")).toBe(
			"Dismiss workspace setup reminder",
		);
	});
});
