import Link from "next/link";
import {
	legalContentConfig,
	legalContentConfigured,
} from "@/lib/legal-config";

export function LegalDocument({ kind }: { kind: "terms" | "privacy" }) {
	const configured = legalContentConfigured();
	const content =
		kind === "terms"
			? legalContentConfig.termsContent
			: legalContentConfig.privacyContent;

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
						provide
						<code className="mx-1">NEXT_PUBLIC_LEGAL_TERMS_CONTENT</code> and
						<code className="mx-1">NEXT_PUBLIC_LEGAL_PRIVACY_CONTENT</code>,
						and obtain jurisdiction-specific legal review before launch.
					</p>
				</section>
			) : (
				<div className="mt-8 space-y-8 leading-7">
					<div className="whitespace-pre-wrap">{content}</div>
					<p className="border-t pt-5 text-sm text-muted-foreground">
						This content was supplied by {legalContentConfig.entityName}.
						Jurisdiction-specific legal review remains the operator&apos;s
						responsibility. Questions may be sent to{" "}
						{legalContentConfig.contactEmail}.
					</p>
				</div>
			)}
		</main>
	);
}
