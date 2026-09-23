import type { Metadata } from "next";
import { LegalDocument } from "@/components/legal-document";
import { readLegalContent } from "@/lib/legal-content";

export const metadata: Metadata = { title: "Terms of Service" };

export default async function Page() {
	return <LegalDocument kind="terms" legal={await readLegalContent()} />;
}
