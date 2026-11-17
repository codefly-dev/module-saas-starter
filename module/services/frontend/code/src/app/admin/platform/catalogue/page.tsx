import { AccessDenied } from "@/components/access-denied";
import { RoleGate } from "@/components/auth/role-gate";
import { PlatformCataloguePage } from "@/features/platform-catalogue";

export default function Page() {
	return (
		<RoleGate
			require="super_admin"
			fallback={
				<AccessDenied
					title="Super administrator required"
					description="The Catalogue spans every organization's installations and is restricted to platform super administrators."
				/>
			}
		>
			<PlatformCataloguePage />
		</RoleGate>
	);
}
