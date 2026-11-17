import { AccessDenied } from "@/components/access-denied";
import { RoleGate } from "@/components/auth/role-gate";
import { UnattendedWorkPlayground } from "@/features/unattended-work";

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
 */
export default function Page() {
	return (
		<RoleGate
			require="admin"
			fallback={
				<AccessDenied
					title="Organization administrator required"
					description="Binding the identity unattended work runs under reaches the delegations of the organization you are acting in, and is restricted to its administrators."
				/>
			}
		>
			<UnattendedWorkPlayground />
		</RoleGate>
	);
}
