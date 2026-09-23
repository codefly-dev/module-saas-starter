import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import {
	type ProductFeatures,
	UNCONFIGURED_PUBLIC_RUNTIME_CONFIG,
} from "@/lib/public-runtime-config";
import { PublicRuntimeConfigProvider } from "@/lib/public-runtime-config-provider";
import { OptionalProductFeature } from "../optional-product-feature";

afterEach(cleanup);

function withFeatures(
	features: Partial<ProductFeatures>,
	child: React.ReactNode,
) {
	return (
		<PublicRuntimeConfigProvider
			config={{
				...UNCONFIGURED_PUBLIC_RUNTIME_CONFIG,
				productFeatures: {
					...UNCONFIGURED_PUBLIC_RUNTIME_CONFIG.productFeatures,
					...features,
				},
			}}
		>
			{child}
		</PublicRuntimeConfigProvider>
	);
}

it.each(["sso", "subscriptions", "entitlements"] as const)(
	"does not mount disabled %s API-backed screens",
	(feature) => {
		const Child = vi.fn(() => <div>Provider screen</div>);
		render(
			withFeatures(
				{},
				<OptionalProductFeature feature={feature}>
					<Child />
				</OptionalProductFeature>,
			),
		);
		expect(Child).not.toHaveBeenCalled();
		expect(screen.getByRole("heading").textContent).toContain("is not enabled");
	},
);
it("mounts a screen the deployment's configuration enables", () => {
	render(
		withFeatures(
			{ subscriptions: true },
			<OptionalProductFeature feature="subscriptions">
				<div>Provider screen</div>
			</OptionalProductFeature>,
		),
	);
	expect(screen.getByText("Provider screen")).toBeTruthy();
});
