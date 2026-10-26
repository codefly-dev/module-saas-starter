import { RoleGate } from "@/components/auth/role-gate";
import { InstallationSetupPage } from "@/features/solution-installations";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";

function AccessDenied() {
	return (
		<div className="rounded-lg border p-6">
			<h1 data-slot="page-title" className="type-page-title">
				Organization administrator required
			</h1>
			<p className="mt-2 text-sm text-muted-foreground">
				Setting up a module or solution is done by organization administrators
				and owners, or platform super administrators.
			</p>
		</div>
	);
}

/**
 * One installation's setup page, under `Admin → Modules & solutions`. Gated by
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
			fallback={<AccessDenied />}
		>
			<InstallationSetupPage installationId={installationId} />
		</RoleGate>
	);
}
