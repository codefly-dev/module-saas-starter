import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
	InvitationDeliveryStatus,
	type Invitation as InvitationMessage,
	InvitationRole,
	InvitationStatus,
} from "@/gen/saas/accounts/v1/invitations_pb";
import type { Invitation } from "./types";

export function transformInvitation(message: InvitationMessage): Invitation {
	return {
		id: message.id,
		orgId: message.orgId,
		inviterId: message.inviterId,
		inviterDisplayName: message.inviterDisplayName,
		email: message.email,
		role: message.role,
		status: message.status,
		deliveryStatus: message.deliveryStatus,
		expiresAt: message.expiresAt
			? timestampDate(message.expiresAt).toISOString()
			: undefined,
		createdAt: message.createdAt
			? timestampDate(message.createdAt).toISOString()
			: undefined,
		lastSentAt: message.lastSentAt
			? timestampDate(message.lastSentAt).toISOString()
			: undefined,
		sendCount: message.sendCount,
	};
}

export function formatInvitationStatus(status: InvitationStatus | number): {
	label: string;
	variant: "default" | "secondary" | "destructive" | "outline";
} {
	switch (status) {
		case InvitationStatus.PENDING:
			return { label: "Pending", variant: "default" };
		case InvitationStatus.ACCEPTED:
			return { label: "Accepted", variant: "secondary" };
		case InvitationStatus.REVOKED:
			return { label: "Revoked", variant: "destructive" };
		case InvitationStatus.EXPIRED:
			return { label: "Expired", variant: "outline" };
		default:
			return { label: "Unknown", variant: "outline" };
	}
}

export function formatInvitationRole(role: InvitationRole): string {
	switch (role) {
		case InvitationRole.MEMBER:
			return "Member";
		case InvitationRole.ADMIN:
			return "Admin";
		default:
			return "Unknown";
	}
}

export function formatDeliveryStatus(status: InvitationDeliveryStatus): string {
	switch (status) {
		// No email was sent for this invitation — the deployment sends none, or
		// the invitee opted out. The link is the delivery: "Copy link" issues it.
		case InvitationDeliveryStatus.DISABLED:
			return "Email not sent — share the link";
		case InvitationDeliveryStatus.QUEUED:
			return "Queued";
		case InvitationDeliveryStatus.SENT:
			return "Sent";
		case InvitationDeliveryStatus.DELIVERED:
			return "Delivered";
		case InvitationDeliveryStatus.BOUNCED:
			return "Bounced";
		case InvitationDeliveryStatus.COMPLAINED:
			return "Complained";
		default:
			return "Unknown";
	}
}
