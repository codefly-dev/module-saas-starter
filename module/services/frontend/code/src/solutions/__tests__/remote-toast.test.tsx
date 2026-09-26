import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

vi.mock("@module-federation/runtime", () => ({
	createInstance: () => ({ registerRemotes: vi.fn(), loadRemote: vi.fn() }),
}));

import { Toaster } from "@/components/ui/sonner";
import { CODEFLY_KIT_SHARED } from "../SolutionOutlet";

afterEach(cleanup);

// A runtime-loaded remote resolves `@codefly-dev/ui/layout` to the module the
// host publishes into the shared scope (a sealed singleton). Its `toast` must
// write to the store the host's mounted Toaster reads, or a remote's
// notification is lost without a trace.
it("shows a remote's toast in the host's Toaster through the shared kit", async () => {
	render(<Toaster />);
	const shared = CODEFLY_KIT_SHARED["@codefly-dev/ui/layout"];
	expect(shared.shareConfig.singleton).toBe(true);
	const layout = shared.lib() as typeof import("@codefly-dev/ui/layout");
	await act(async () => {
		layout.toast.error("Couldn't delete this item: HTTP 503. Try again.");
	});
	expect(
		await screen.findByText("Couldn't delete this item: HTTP 503. Try again."),
	).toBeTruthy();
});
