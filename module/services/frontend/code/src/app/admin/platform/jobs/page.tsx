import { AccessDenied } from "@/components/access-denied";
import { RoleGate } from "@/components/auth/role-gate";
import { JobOperationsPage } from "@/features/job-operations";

export default function Page() {
	return (
		<RoleGate
			require="super_admin"
			fallback={
				<AccessDenied
					title="Super administrator required"
					description="Job operations span all tenants and are restricted to platform super administrators."
				/>
			}
		>
			<JobOperationsPage />
		</RoleGate>
	);
}
