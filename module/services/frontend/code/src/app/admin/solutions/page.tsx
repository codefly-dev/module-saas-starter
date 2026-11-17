import { Suspense } from "react";
import { AccessDenied } from "@/components/access-denied";
import { RoleGate } from "@/components/auth/role-gate";
import { SolutionInstallationsRoute } from "@/features/solution-installations";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";

export default function Page() {
	return (
		<RoleGate
			requirePermission={PERMISSIONS.ROLES_WRITE}
			fallback={
				<AccessDenied
					title="Organization administrator required"
					description="Solutions are managed by organization administrators and owners, or platform super administrators."
				/>
			}
		>
			<Suspense fallback={null}>
				<SolutionInstallationsRoute />
			</Suspense>
		</RoleGate>
	);
}
