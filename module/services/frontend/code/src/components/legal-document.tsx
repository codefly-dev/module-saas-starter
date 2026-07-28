import Link from "next/link";

export function LegalDocument({ kind }: { kind: "terms" | "privacy" }) {
	const operator = process.env.NEXT_PUBLIC_LEGAL_ENTITY_NAME;
	const contact = process.env.NEXT_PUBLIC_LEGAL_CONTACT_EMAIL;
	const configured = Boolean(operator && contact);

	return (
		<main className="mx-auto min-h-screen max-w-3xl px-5 py-12 sm:py-20">
			<Link
				href="/"
				className="text-sm text-muted-foreground hover:text-foreground"
			>
				← Back
			</Link>
			<h1 className="mt-8 text-4xl font-bold tracking-tight">
				{kind === "terms" ? "Terms of Service" : "Privacy Policy"}
			</h1>
			<p className="mt-3 text-sm text-muted-foreground">
				Starter policy version: 2026-07-28
			</p>
			{!configured ? (
				<section className="mt-8 rounded-xl border border-destructive/40 bg-destructive/5 p-5">
					<h2 className="font-semibold">Legal content is not configured</h2>
					<p className="mt-2 text-sm leading-6">
						This starter cannot supply production legal terms. Configure
						<code className="mx-1">NEXT_PUBLIC_LEGAL_ENTITY_NAME</code> and
						<code className="mx-1">NEXT_PUBLIC_LEGAL_CONTACT_EMAIL</code>,
						replace this starter text, and obtain jurisdiction-specific legal
						review before launch.
					</p>
				</section>
			) : (
				<div className="mt-8 space-y-8 leading-7">
					<section>
						<h2 className="text-xl font-semibold">
							Starter text requiring legal review
						</h2>
						<p className="mt-2">
							This page is a technical placeholder, not legal advice. It must be
							reviewed and replaced for the product, customers, data, vendors,
							and jurisdictions operated by {operator}.
						</p>
					</section>
					{kind === "terms" ? (
						<>
							<section>
								<h2 className="text-xl font-semibold">Service use</h2>
								<p className="mt-2">
									Describe account eligibility, acceptable use, customer
									responsibilities, service availability, billing, termination,
									and dispute terms here.
								</p>
							</section>
							<section>
								<h2 className="text-xl font-semibold">Contact</h2>
								<p className="mt-2">
									Questions about these terms may be sent to {contact}.
								</p>
							</section>
						</>
					) : (
						<>
							<section>
								<h2 className="text-xl font-semibold">Data and purposes</h2>
								<p className="mt-2">
									Describe the personal data collected for necessary service
									operation, optional analytics, and optional marketing. List
									processors, legal bases, retention periods, transfers, and
									individual rights applicable to the deployed product.
								</p>
							</section>
							<section>
								<h2 className="text-xl font-semibold">Choices and contact</h2>
								<p className="mt-2">
									Optional analytics and marketing choices can be changed in
									privacy settings. Privacy requests may be sent to {contact}.
								</p>
							</section>
						</>
					)}
				</div>
			)}
		</main>
	);
}
