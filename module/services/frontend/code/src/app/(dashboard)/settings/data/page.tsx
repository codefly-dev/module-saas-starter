import { DataPrivacyPage } from "@/features/gdpr/ui/data-privacy-page";
import { capabilityStateAtLeast } from "@/features/trust/model/capabilities";
import { loadPublicCapabilities } from "@/features/trust/model/capabilities.server";

export const dynamic = "force-dynamic";

export default function Page() {
	const capabilities = loadPublicCapabilities();
	const exportState = capabilities.find(
		(capability) => capability.id === "privacy.export-artifact",
	)?.state;
	const deletionState = capabilities.find(
		(capability) => capability.id === "privacy.deletion-completion",
	)?.state;
	return (
		<DataPrivacyPage
			exportAvailable={
				exportState !== undefined &&
				capabilityStateAtLeast(exportState, "operationally_verified")
			}
			deletionAvailable={
				deletionState !== undefined &&
				capabilityStateAtLeast(deletionState, "operationally_verified")
			}
		/>
	);
}
