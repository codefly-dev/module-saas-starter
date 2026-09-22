import { BillingAdminPage } from "@/features/billing/ui/billing-admin-page";
import { OptionalProductFeature } from "@/components/optional-product-feature";

export default function Page() {
	return (
		<OptionalProductFeature feature="subscriptions">
			<BillingAdminPage />
		</OptionalProductFeature>
	);
}
