import { SSOAdminPage } from "@/features/sso-admin/ui/sso-admin-page";
import { OptionalProductFeature } from "@/components/optional-product-feature";

export default function Page() {
	return (
		<OptionalProductFeature feature="sso">
			<SSOAdminPage />
		</OptionalProductFeature>
	);
}
