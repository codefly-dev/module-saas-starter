import { AccessDenied } from "@/components/access-denied";
import { RoleGate } from "@/components/auth/role-gate";
import { ModuleAuthorityPage } from "@/features/module-authority";

export default function Page() {
	return (
		<RoleGate
			require="super_admin"
			fallback={
				<AccessDenied
					title="Super administrator required"
					description="The module envelope bounds the authority of every module on this host, and is read and written only by platform super administrators."
				/>
			}
		>
			<ModuleAuthorityPage />
		</RoleGate>
	);
}
