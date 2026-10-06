import { RoleGate } from "@/components/auth/role-gate";
import { PlatformCataloguePage } from "@/features/platform-catalogue";

function AccessDenied() {
	return (
		<div className="rounded-lg border p-6">
			<h1 data-slot="page-title" className="type-page-title">
				Super administrator required
			</h1>
			<p className="mt-2 text-sm text-muted-foreground">
				The Catalogue spans every organization&apos;s installations and is
				restricted to platform super administrators.
			</p>
		</div>
	);
}

export default function Page() {
	return (
		<RoleGate require="super_admin" fallback={<AccessDenied />}>
			<PlatformCataloguePage />
		</RoleGate>
	);
}
