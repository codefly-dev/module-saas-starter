import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { OptionalProductFeature } from "../optional-product-feature";

const features = vi.hoisted(() => ({
	subscriptions: false,
	sso: false,
	entitlements: false,
}));
vi.mock("@/lib/product-features", () => ({ productFeatures: features }));
afterEach(cleanup);
beforeEach(() => {
	features.subscriptions = false;
	features.sso = false;
	features.entitlements = false;
});

it.each(["sso", "subscriptions", "entitlements"] as const)(
	"does not mount disabled %s API-backed screens",
	(feature) => {
		const Child = vi.fn(() => <div>Provider screen</div>);
		render(
			<OptionalProductFeature feature={feature}>
				<Child />
			</OptionalProductFeature>,
		);
		expect(Child).not.toHaveBeenCalled();
		expect(screen.getByRole("heading").textContent).toContain("is not enabled");
	},
);
it("mounts an explicitly enabled screen", () => {
	features.subscriptions = true;
	render(
		<OptionalProductFeature feature="subscriptions">
			<div>Provider screen</div>
		</OptionalProductFeature>,
	);
	expect(screen.getByText("Provider screen")).toBeTruthy();
});
