/** Clean domain types for the Notifications feature. */

export type NotificationType =
	| "info"
	| "success"
	| "warning"
	| "error"
	| "invite"
	| "mention"
	| "system";

export interface Notification {
	id: string;
	title: string;
	body: string;
	type: NotificationType;
	read: boolean;
	createdAt: string;
	linkUrl?: string;
}
