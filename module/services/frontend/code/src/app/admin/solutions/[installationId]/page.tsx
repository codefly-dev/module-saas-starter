import { AccessDenied } from "@/components/access-denied";
import { RoleGate } from "@/components/auth/role-gate";
import { InstallationSetupPage } from "@/features/solution-installations";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";

/**
 * One installation's setup page, under `Admin → Solutions`. Gated by
 * the same permission as that page: it shows the same organisation's
 * installations and performs the same host acts.
 */
export default async function Page({
	params,
}: {
	params: Promise<{ installationId: string }>;
}) {
	const { installationId } = await params;
	return (
		<RoleGate
			requirePermission={PERMISSIONS.ROLES_WRITE}
			fallback={
				<AccessDenied
					title="Organization administrator required"
					description="Setting up a module or solution is done by organization administrators and owners, or platform super administrators."
				/>
			}
		>
			<InstallationSetupPage installationId={installationId} />
		</RoleGate>
	);
}
