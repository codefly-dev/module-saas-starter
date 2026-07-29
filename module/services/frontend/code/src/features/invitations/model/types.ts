import type {
	InvitationDeliveryStatus,
	InvitationRole,
	InvitationStatus,
} from "@/gen/saas/accounts/v1/invitations_pb";

export interface Invitation {
	id: string;
	orgId: string;
	inviterId: string;
	inviterDisplayName: string;
	email: string;
	role: InvitationRole;
	status: InvitationStatus;
	deliveryStatus: InvitationDeliveryStatus;
	expiresAt?: string;
	createdAt?: string;
	lastSentAt?: string;
	sendCount: number;
}
