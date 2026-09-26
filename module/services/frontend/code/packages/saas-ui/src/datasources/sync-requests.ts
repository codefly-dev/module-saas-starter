/**
 * "A sync of this source was just requested" — announced by the datasource
 * client whenever it enqueues one (Sync now, a reconnect, or the first sync a
 * connect starts), so a view that shows the source's progress can start
 * watching closely at once instead of on its next slow poll.
 *
 * The host names no listener: whoever renders progress under a source (the
 * panel's `renderSourceDetail` slot) subscribes by source id. The registry is
 * keyed on `globalThis`, so a solution remote that bundles its own copy of this
 * kit still hears the host panel's announcements.
 */

type Listener = (sourceId: string) => void;

const registryKey = Symbol.for("codefly.saas-ui.source-sync-requests");

function listeners(): Set<Listener> {
	const scope = globalThis as unknown as Record<symbol, Set<Listener> | undefined>;
	let set = scope[registryKey];
	if (!set) {
		set = new Set<Listener>();
		scope[registryKey] = set;
	}
	return set;
}

/** Calls `listener` with the source id of every sync requested from now on; returns the unsubscribe. */
export function onSourceSyncRequested(listener: Listener): () => void {
	const set = listeners();
	set.add(listener);
	return () => {
		set.delete(listener);
	};
}

/** Announces that a sync of `sourceId` was just enqueued. A throwing listener never stops the others. */
export function notifySourceSyncRequested(sourceId: string): void {
	if (!sourceId) return;
	for (const listener of [...listeners()]) {
		try {
			listener(sourceId);
		} catch {
			// A subscriber's failure is its own; the sync is already enqueued.
		}
	}
}
