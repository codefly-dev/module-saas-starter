import { RoleGate } from "@/components/auth/role-gate";
import { PluginTransportPlayground } from "@/features/plugin-transport";

function AccessDenied() {
	return (
		<div className="rounded-lg border p-6">
			<h1 data-slot="page-title" className="type-page-title">
				Super administrator required
			</h1>
			<p className="mt-2 text-sm text-muted-foreground">
				Driving the plugin transport sends calls to every installed plugin
				backend under your own credential and is restricted to platform super
				administrators.
			</p>
		</div>
	);
}

export default function Page() {
	return (
		<RoleGate require="super_admin" fallback={<AccessDenied />}>
			<PluginTransportPlayground />
		</RoleGate>
	);
}
