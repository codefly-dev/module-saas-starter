import { EntitlementsPage } from "@/features/platform/ui/entitlements-page";
import { OptionalProductFeature } from "@/components/optional-product-feature";
export default function Page() {
	return (
		<OptionalProductFeature feature="entitlements">
			<EntitlementsPage />
		</OptionalProductFeature>
	);
}
