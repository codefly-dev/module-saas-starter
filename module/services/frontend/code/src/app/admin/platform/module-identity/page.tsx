import { RoleGate } from "@/components/auth/role-gate";
import { ModuleTenantIdentityPlayground } from "@/features/module-tenant-identity";

function AccessDenied() {
	return (
		<div className="rounded-lg border p-6">
			<h1 data-slot="page-title" className="type-page-title">
				Super administrator required
			</h1>
			<p className="mt-2 text-sm text-muted-foreground">
				Resolving what a module&apos;s capability would carry reads any
				organization&apos;s installation scope and is restricted to platform
				super administrators.
			</p>
		</div>
	);
}

/**
 * The playground for a module identity minted for one tenant.
 *
 * Under `/admin/platform/`, which is where it belongs and where the route
 * catalog derives `super_admin` from — the same boundary `ResolveModuleTenantIdentity`
 * declares. It reads any organization's installation scope, which is
 * cross-tenant authority however read-only it is, so this page's gate and the
 * RPC's `platform_role` are one answer rather than two.
 */
export default function Page() {
	return (
		<RoleGate require="super_admin" fallback={<AccessDenied />}>
			<ModuleTenantIdentityPlayground />
		</RoleGate>
	);
}
