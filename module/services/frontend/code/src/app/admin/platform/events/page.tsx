import { AccessDenied } from "@/components/access-denied";
import { RoleGate } from "@/components/auth/role-gate";
import { EventOperationsPage } from "@/features/event-operations";

export default function Page() {
	return (
		<RoleGate
			require="super_admin"
			fallback={
				<AccessDenied
					title="Super administrator required"
					description="Event operations span all tenants and are restricted to platform super administrators."
				/>
			}
		>
			<EventOperationsPage />
		</RoleGate>
	);
}
