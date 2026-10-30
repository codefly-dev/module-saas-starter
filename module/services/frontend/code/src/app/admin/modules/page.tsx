import { RoleGate } from "@/components/auth/role-gate";
import { SolutionInstallationsPage } from "@/features/solution-installations";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";

/** Advanced supporting-module administration; not part of solution installation. */
export default function Page() {
	return (
		<RoleGate
			requirePermission={PERMISSIONS.ROLES_WRITE}
			fallback={<p>Organization administrator required</p>}
		>
			<SolutionInstallationsPage supportingModules />
		</RoleGate>
	);
}
