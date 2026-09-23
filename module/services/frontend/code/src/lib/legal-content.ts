import "server-only";

import { getWorkspaceConfiguration } from "codefly";
import { connection } from "next/server";

import { type LegalContentConfig, resolveLegalContent } from "./legal-config";

/**
 * The operator's legal content: the Codefly `legal` workspace configuration,
 * read from the running process. Awaiting `connection()` keeps the read at
 * request time, so a deployed image serves the content of the environment it
 * runs in rather than whatever its build saw, which is nothing.
 */
export async function readLegalContent(): Promise<LegalContentConfig> {
	await connection();
	const value = (key: string) => getWorkspaceConfiguration("legal", key);
	return resolveLegalContent({
		entityName: value("NEXT_PUBLIC_LEGAL_ENTITY_NAME"),
		contactEmail: value("NEXT_PUBLIC_LEGAL_CONTACT_EMAIL"),
		termsContent: value("NEXT_PUBLIC_LEGAL_TERMS_CONTENT"),
		privacyContent: value("NEXT_PUBLIC_LEGAL_PRIVACY_CONTENT"),
	});
}
