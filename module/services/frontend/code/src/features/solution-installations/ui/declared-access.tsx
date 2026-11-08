import type { ReviewedOffer } from "@/gen/host/installation/v1/installation_pb";
import { offerScopeText } from "../model/install-review";

/**
 * The machine access an installation receives, as the authority's offer
 * declares it: its signed installation role. One rendering for a solution's
 * review and a module page's, so the two reviews cannot describe the same
 * declaration differently.
 */
export function DeclaredAccess({
	subject,
	installationRole,
	offer,
}: {
	subject: "solution" | "module";
	installationRole: string;
	offer: Pick<ReviewedOffer, "roles">;
}) {
	const machineRole = offer.roles.find(
		(role) => role.name === installationRole,
	);
	const heading = subject === "solution" ? "Solution access" : "Module access";
	return (
		<section aria-label={heading} className="space-y-2">
			<h3 className="font-medium">{heading}</h3>
			{installationRole === "none" ? (
				<p>
					This {subject} receives no standing machine access.
					{subject === "solution" &&
						" Setup receives only temporary permission to complete this installation."}
				</p>
			) : !installationRole ? (
				<p>
					This {subject} declares no installation access. The host refuses to
					install it until its owner declares one.
				</p>
			) : (
				<>
					<p>
						The {subject} declares its installation access as “
						{machineRole?.name ?? installationRole}”.
					</p>
					<ul className="list-disc pl-5">
						{machineRole?.scopes.map(offerScopeText).map((text) => (
							<li key={text}>{text}</li>
						))}
					</ul>
					{machineRole?.scopes.length === 0 && (
						<p>This declared role grants no data access.</p>
					)}
				</>
			)}
		</section>
	);
}

/**
 * The most each declared call may ask, one line per module and access, with a
 * call reached on two faces shown once and no binding identifier.
 */
export function DeclaredOperations({
	offer,
}: {
	offer: Pick<ReviewedOffer, "ceiling">;
}) {
	const lines = [
		...new Set(
			offer.ceiling.map(
				(binding) =>
					`${binding.module}: ${binding.scopes.map(offerScopeText).join("; ") || "no data access"}`,
			),
		),
	];
	if (!lines.length) return null;
	return (
		<>
			<p>Declared operations, still subject to the caller’s assigned access:</p>
			<ul className="list-disc pl-5">
				{lines.map((line) => (
					<li key={line}>{line}</li>
				))}
			</ul>
		</>
	);
}
