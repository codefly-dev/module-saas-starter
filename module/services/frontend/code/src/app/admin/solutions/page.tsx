import { RoleGate } from "@/components/auth/role-gate";
import { SolutionInstallationsPage } from "@/features/solution-installations";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";

function AccessDenied() {
	return (
		<div className="rounded-lg border p-6">
			<h1 data-slot="page-title" className="type-page-title">
				Organization administrator required
			</h1>
			<p className="mt-2 text-sm text-muted-foreground">
				Solutions are managed by organization administrators and owners, or
				platform super administrators.
			</p>
		</div>
	);
}

export default function Page() {
	return (
		<RoleGate
			requirePermission={PERMISSIONS.ROLES_WRITE}
			fallback={<AccessDenied />}
		>
			<SolutionInstallationsPage />
		</RoleGate>
	);
}
