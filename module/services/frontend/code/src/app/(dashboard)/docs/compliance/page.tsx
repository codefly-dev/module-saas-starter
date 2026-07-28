import type { PublicCapability } from "@/features/trust/model/capabilities";
import { loadPublicCapabilities } from "@/features/trust/model/capabilities.server";
import { ComplianceSection as Section } from "./compliance-section";

export const dynamic = "force-dynamic";

export default function CompliancePage() {
	const capabilities = loadPublicCapabilities();
	const available = capabilities.filter(
		(capability) => capability.state !== "absent",
	);
	const adopterOwned = capabilities.filter(
		(capability) => capability.state === "absent",
	);

	return (
		<div className="space-y-6">
			<div>
				<h1 className="text-2xl font-bold tracking-tight">
					Security &amp; Trust Readiness
				</h1>
				<p className="text-muted-foreground mt-1">
					Starter-provided controls and the production responsibilities that an
					adopter must configure, exercise, and review.
				</p>
			</div>

			<div className="rounded-xl border border-amber-500/30 bg-amber-500/10 p-4 text-sm">
				<p className="font-medium">
					Starter defaults are not production evidence.
				</p>
				<p className="mt-1 text-muted-foreground">
					This view describes the unconfigured distribution. Deployment
					behavior, provider controls, legal commitments, recovery exercises,
					and external assurance must be evidenced for each environment before
					they are represented as available.
				</p>
			</div>

			<div className="space-y-4">
				<Section
					icon="security"
					title="Current capability state"
					description="Source, configuration, and evidence remain distinct"
					defaultOpen
				>
					<CapabilityList capabilities={available} />
				</Section>

				<Section
					icon="lock"
					title="Production and adopter responsibilities"
					description="Not supplied or verified by the starter default"
				>
					<CapabilityList capabilities={adopterOwned} />
				</Section>

				<Section
					icon="document"
					title="Evidence and review model"
					description="What is required before a stronger claim can be published"
				>
					<p>
						Each evidence record is bound to one capability, environment, and
						scope. It identifies an owner, verifier, private source or artifact,
						performed time, review or expiry time, and status.
					</p>
					<ul>
						<li>
							An enabled provider or local fixture establishes configuration,
							not production verification.
						</li>
						<li>
							Expired, revoked, rejected, differently scoped, or
							differently-environmented evidence cannot promote a claim.
						</li>
						<li>
							Public summaries omit private artifact locations, reviewers, and
							other sensitive evidence metadata.
						</li>
						<li>
							Legal, privacy, security, and contractual text requires adopter
							review even when the starter supplies a related technical control.
						</li>
					</ul>
				</Section>
			</div>
		</div>
	);
}

function CapabilityList({
	capabilities,
}: {
	capabilities: PublicCapability[];
}) {
	return (
		<ul className="not-prose space-y-3">
			{capabilities.map((capability) => (
				<li key={capability.id} className="rounded-lg border p-3">
					<div className="flex flex-wrap items-center justify-between gap-2">
						<span className="font-medium">{capability.title}</span>
						<span className="rounded-full border px-2 py-0.5 text-xs">
							{capability.label}
						</span>
					</div>
					<p className="mt-1 text-xs text-muted-foreground">
						Responsibility: {capability.responsibility}
					</p>
					{capability.summary ? (
						<p className="mt-2 text-sm">{capability.summary}</p>
					) : (
						<p className="mt-2 text-sm text-muted-foreground">
							No customer-facing availability claim is published without the
							required current evidence.
						</p>
					)}
				</li>
			))}
		</ul>
	);
}
