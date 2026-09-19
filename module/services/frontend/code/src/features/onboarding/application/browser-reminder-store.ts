// Whether the viewer has dismissed the "finish workspace setup" reminder.
//
// Per browser, not per account: the reminder is a nudge about optional setup
// that never blocks the product, so dismissing it is a viewing preference rather
// than a fact about the organization. Sending it to the server would make one
// person's "not now" everyone's.
//
// Keyed BY ORGANIZATION. A viewer who belongs to two workspaces has finished
// setup in neither by dismissing the reminder in one, and a single flag would
// hide the nudge in the workspace that still needs it.
//
// Framework-free, like every other module under `application/`.

const REMINDER_KEY = "saas-starter:onboarding-reminder-dismissed";

export interface OnboardingReminderStore {
	isDismissed(organizationId: string): boolean;
	dismiss(organizationId: string): void;
	/** `useSyncExternalStore` contract: re-render subscribers after a dismiss. */
	subscribe(listener: () => void): () => void;
}

/**
 * `storage` is optional because there is not always one: this runs during SSR,
 * and a private window or blocked site data makes even reading it throw. The
 * reminder then simply shows, which is the safe direction — a nudge that
 * appears once more costs nothing, one that hides itself wrongly costs the
 * setup.
 */
export function createBrowserOnboardingReminderStore(
	storage: Pick<Storage, "getItem" | "setItem"> | null,
): OnboardingReminderStore {
	const listeners = new Set<() => void>();
	// Read once and keep it: `useSyncExternalStore` compares snapshots on every
	// render, so going to storage each time would be a parse per render.
	let dismissed: Set<string> | null = null;

	function read(): Set<string> {
		if (dismissed) return dismissed;
		dismissed = new Set<string>();
		try {
			const raw = storage?.getItem(REMINDER_KEY);
			const parsed: unknown = raw ? JSON.parse(raw) : null;
			if (Array.isArray(parsed))
				for (const id of parsed)
					if (typeof id === "string" && id.length > 0) dismissed.add(id);
		} catch {
			// Unreadable or malformed: treat it as nothing dismissed rather than
			// letting a bad value break the page around it.
		}
		return dismissed;
	}

	return {
		isDismissed(organizationId) {
			return organizationId.length > 0 && read().has(organizationId);
		},
		dismiss(organizationId) {
			if (organizationId.length === 0) return;
			const current = read();
			if (current.has(organizationId)) return;
			current.add(organizationId);
			try {
				storage?.setItem(REMINDER_KEY, JSON.stringify([...current]));
			} catch {
				// Storage refused the write. The dismissal still holds for this
				// page, it just will not outlive a reload.
			}
			for (const listener of listeners) listener();
		},
		subscribe(listener) {
			listeners.add(listener);
			return () => {
				listeners.delete(listener);
			};
		},
	};
}
