import { RoleGate } from "@/components/auth/role-gate";
import { UnattendedWorkPlayground } from "@/features/unattended-work";

function AccessDenied() {
	return (
		<div className="rounded-lg border p-6">
			<h1 data-slot="page-title" className="type-page-title">
				Organization administrator required
			</h1>
			<p className="mt-2 text-sm text-muted-foreground">
				Binding the identity unattended work runs under reaches the delegations
				of the organization you are acting in, and is restricted to its
				administrators.
			</p>
		</div>
	);
}

/**
 * The playground for whose authority work no person started runs under.
 *
 * Gated on organization admin, which is what the RPCs behind it actually
 * require: `GetSourceTriggerDeclaration`, `DelegateSourceTriggers` and
 * `RevokeSourceDelegation` are all `ORG_ADMIN` and all org-scoped, and
 * `ReconcileSource` is an org member holding `datasource:invoke`. The page
 * asked for a platform super administrator and said it reached every tenant's
 * delegations, which was wrong in both directions: it hid an org admin's own
 * organization from them, and it described a reach no RPC behind it has.
 *
 * IT LIVES UNDER `/admin/`, NOT `/admin/platform/`, and that is the fix rather
 * than a tidy-up (issue #1121, N5). The route catalog derives a page's access
 * boundary from its path — everything under `admin/platform` is `super_admin`
 * — so while this page sat there, the catalog said `super_admin` and the gate
 * below said `admin`: two access declarations for one page, disagreeing, with
 * nothing to reconcile them. Moving it makes the derivation agree by
 * construction instead of by a special case in the generator, and it gives
 * organization administrators a navigation entry they can actually reach
 * (`admin-unattended-work` in frontend.bindings.codefly.yaml).
 */
export default function Page() {
	return (
		<RoleGate require="admin" fallback={<AccessDenied />}>
			<UnattendedWorkPlayground />
		</RoleGate>
	);
}
