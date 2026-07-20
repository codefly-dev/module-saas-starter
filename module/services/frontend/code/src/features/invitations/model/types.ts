// Pure domain types for invitations. No React imports.

export type InvitationStatusValue =
	| "pending"
	| "accepted"
	| "revoked"
	| "expired"
	| "unspecified";

export interface Invitation {
	id: string;
	orgId: string;
	inviterId: string;
	email: string;
	role: string;
	status: number;
	expiresAt?: string;
	createdAt?: string;
}
