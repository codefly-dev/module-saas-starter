import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PluginAvailabilityError } from "../src/availability.js";
import {
	PluginErrorBoundary,
	type PluginErrorBoundaryFallbackProps,
} from "../src/ui.js";

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

function silenceReactError(): void {
	vi.spyOn(console, "error").mockImplementation(() => undefined);
}

function Fallback({ failure, retry }: PluginErrorBoundaryFallbackProps) {
	return (
		<button type="button" onClick={retry}>
			{failure.state}:{failure.code}
		</button>
	);
}

function Boundary({ children }: { children: ReactNode }) {
	return (
		<PluginErrorBoundary fallback={Fallback}>{children}</PluginErrorBoundary>
	);
}

describe("public plugin error boundary", () => {
	it("contains a typed availability error and exposes only safe state", () => {
		silenceReactError();
		function ThrowingContribution(): never {
			throw new PluginAvailabilityError("unavailable", {
				code: "backend_unavailable",
				cause: new Error("private endpoint detail"),
			});
		}

		render(
			<Boundary>
				<ThrowingContribution />
			</Boundary>,
		);
		expect(screen.getByText("unavailable:backend_unavailable")).toBeTruthy();
		expect(screen.queryByText(/private endpoint detail/)).toBeNull();
	});

	it("contains unknown render errors with a stable generic code", () => {
		silenceReactError();
		function ThrowingContribution(): never {
			throw new Error("private render detail");
		}

		render(
			<Boundary>
				<ThrowingContribution />
			</Boundary>,
		);
		expect(screen.getByText("failed:render_failed")).toBeTruthy();
		expect(screen.queryByText(/private render detail/)).toBeNull();
	});

	it("lets the host retry by remounting the contained contribution", () => {
		silenceReactError();
		let shouldThrow = true;
		function RecoveringContribution() {
			if (shouldThrow) throw new Error("transient");
			return <span>Recovered contribution</span>;
		}
		function RetryFallback({ retry }: PluginErrorBoundaryFallbackProps) {
			return (
				<button
					type="button"
					onClick={() => {
						shouldThrow = false;
						retry();
					}}
				>
					Retry
				</button>
			);
		}

		render(
			<PluginErrorBoundary fallback={RetryFallback}>
				<RecoveringContribution />
			</PluginErrorBoundary>,
		);
		fireEvent.click(screen.getByRole("button", { name: "Retry" }));
		expect(screen.getByText("Recovered contribution")).toBeTruthy();
	});
});
