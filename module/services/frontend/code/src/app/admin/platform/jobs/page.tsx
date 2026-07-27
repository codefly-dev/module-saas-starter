import { RoleGate } from "@/components/auth/role-gate";
import { JobOperationsPage } from "@/features/job-operations";

function AccessDenied() {
	return (
		<div className="rounded-lg border p-6">
			<h1 className="text-xl font-semibold">Super administrator required</h1>
			<p className="mt-2 text-sm text-muted-foreground">
				Job operations span all tenants and are restricted to platform super
				administrators.
			</p>
		</div>
	);
}

export default function Page() {
	return (
		<RoleGate require="super_admin" fallback={<AccessDenied />}>
			<JobOperationsPage />
		</RoleGate>
	);
}
