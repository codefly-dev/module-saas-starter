import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import {
	type LegalContentConfig,
	legalContentConfigured,
} from "@/lib/legal-config";
import { Button, EmptyState, Page as PageBody, PageHeader } from "@/shared/ui";

export function LegalDocument({
	kind,
	legal,
}: {
	kind: "terms" | "privacy";
	legal: LegalContentConfig;
}) {
	const configured = legalContentConfigured(legal);
	const content = kind === "terms" ? legal.termsContent : legal.privacyContent;
	const title = kind === "terms" ? "Terms of service" : "Privacy policy";

	return (
		<main className="mx-auto min-h-screen w-full max-w-3xl px-4 py-12">
			<PageBody>
				<Button
					variant="ghost"
					size="sm"
					nativeButton={false}
					render={<Link href="/" />}
				>
					<ArrowLeft />
					Back
				</Button>
				<PageHeader title={title} />
				{/* This page is public. A visitor can do nothing about a document
				    that has not been published, so that is all it says. */}
				{!configured ? (
					<EmptyState
						heading="This document is not available yet"
						description={`The ${title.toLowerCase()} has not been published. Please check back later.`}
					/>
				) : (
					<>
						<div className="whitespace-pre-wrap leading-7">{content}</div>
						<p className="border-t pt-4 text-sm text-muted-foreground">
							Provided by {legal.entityName}. Questions about this document can
							be sent to {legal.contactEmail}.
						</p>
					</>
				)}
			</PageBody>
		</main>
	);
}
