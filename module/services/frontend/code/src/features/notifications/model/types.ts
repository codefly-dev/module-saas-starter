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
	orgId: string;
	title: string;
	body: string;
	type: NotificationType;
	read: boolean;
	createdAt: string;
	/** Whether the item has a destination. The destination itself is fetched,
	 *  and re-authorized, only when the link is followed. */
	hasAction: boolean;
}
