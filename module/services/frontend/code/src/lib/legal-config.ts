export interface LegalContentConfig {
	entityName?: string;
	contactEmail?: string;
	termsContent?: string;
	privacyContent?: string;
}

export const legalContentConfig: LegalContentConfig = {
	entityName: process.env.NEXT_PUBLIC_LEGAL_ENTITY_NAME,
	contactEmail: process.env.NEXT_PUBLIC_LEGAL_CONTACT_EMAIL,
	termsContent: process.env.NEXT_PUBLIC_LEGAL_TERMS_CONTENT,
	privacyContent: process.env.NEXT_PUBLIC_LEGAL_PRIVACY_CONTENT,
};

export function legalContentConfigured(
	config: LegalContentConfig = legalContentConfig,
): boolean {
	return Boolean(
		config.entityName?.trim() &&
			config.contactEmail?.trim() &&
			config.termsContent?.trim() &&
			config.privacyContent?.trim(),
	);
}
