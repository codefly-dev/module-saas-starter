/** Clean domain types for the Notifications feature. */

export type NotificationType =
	| "info"
	| "success"
	| "warning"
	| "error"
	| "billing"
	| "security";

export interface Notification {
	id: string;
	title: string;
	body: string;
	type: NotificationType;
	read: boolean;
	createdAt: string;
	actionUrl?: string;
}
