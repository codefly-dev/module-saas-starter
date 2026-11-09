import { scopesText } from "@codefly-dev/saas-ui/solution";
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
	const heading = `What the ${subject} can do on its own`;
	return (
		<section aria-label={heading} className="space-y-2">
			<h3 className="font-medium">{heading}</h3>
			{installationRole === "none" ? (
				<p>
					Nothing: it only acts for the person using it.
					{subject === "solution" &&
						" While it is being set up, it gets temporary permission to finish."}
				</p>
			) : !installationRole ? (
				<p>
					This {subject} declares no installation access. The host refuses to
					install it until its owner declares one.
				</p>
			) : (
				<p>
					It acts on its own as its “{machineRole?.name ?? installationRole}”
					role:{" "}
					{machineRole?.scopes.length
						? `${scopesText(machineRole.scopes)}, only what this installation owns.`
						: "with no access to any data."}
				</p>
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
