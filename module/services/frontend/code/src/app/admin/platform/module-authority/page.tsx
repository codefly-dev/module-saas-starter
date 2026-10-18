import { RoleGate } from "@/components/auth/role-gate";
import { ModuleAuthorityPage } from "@/features/module-authority";

function AccessDenied() {
	return (
		<div className="rounded-lg border p-6">
			<h1 data-slot="page-title" className="type-page-title">
				Super administrator required
			</h1>
			<p className="mt-2 text-sm text-muted-foreground">
				The module envelope bounds the authority of every module on this host,
				and is read and written only by platform super administrators.
			</p>
		</div>
	);
}

export default function Page() {
	return (
		<RoleGate require="super_admin" fallback={<AccessDenied />}>
			<ModuleAuthorityPage />
		</RoleGate>
	);
}
