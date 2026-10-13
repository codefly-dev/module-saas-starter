/**
 * The effect identity a manual reconcile pass commits under, and the record of
 * the passes this browser admitted.
 *
 * `ReconcileSource` is one of the host's operations: idempotency is required,
 * the identity is the CALLER's to mint, and the receipt is read back through
 * `LookupReconcileSource` rather than inferred. So the kit mints the identity
 * here rather than leaving it to the server — an identity the server chose
 * would make every press a new pass, which is the property the operation
 * exists to deny.
 *
 * Nothing here is durable and nothing here is authority. The host owns whether
 * a pass committed; this module owns only which identity a press presents, so
 * that a person pressing twice asks for one pass rather than two full re-sends
 * of a source.
 */

/** What the host says about one reconcile effect. */
export type SourceReconcileStatus = "committed" | "unknown";

/**
 * Which identity each source's next press presents.
 *
 * The rule, stated once: **a press that repeats the pass still being reported
 * reads its receipt back instead of starting another, and the press after that
 * is a new pass.** There is deliberately no clock in it. A window would be a
 * number true only for whoever picked it, and the host has no notion of an
 * identity being spent — it will return the same receipt for the same identity
 * forever — so "when is my intent new" is the client's to answer, and it
 * answers it from presses rather than from time.
 *
 * A ledger belongs to the SURFACE that presses, not to the page: `useReconcileSource`
 * holds one per mounted instance. That is the honest lifetime. A person who
 * navigated away and came back is making a new request, and a module-level
 * ledger would answer their first press with a receipt from the page they left.
 */
export interface ReconcilePassLedger {
	/** The identity to present, and whether it repeats the pass already reported. */
	present(sourceId: string): { effectId: string; repeat: boolean };
	/**
	 * Forget the identity held for a source, so the next press mints a new one.
	 *
	 * Called when a repeat press has surfaced the original receipt: the person
	 * has been told the pass is already recorded, so a further press is a
	 * request they mean. NOT called when a pass fails — that is exactly when the
	 * identity has to survive, so the retry recovers the pass rather than
	 * re-sending the source.
	 */
	retire(sourceId: string): void;
}

export function createReconcilePassLedger(): ReconcilePassLedger {
	const held = new Map<string, string>();
	return {
		present(sourceId) {
			const prior = held.get(sourceId);
			if (prior) return { effectId: prior, repeat: true };
			const effectId = mintEffectId();
			held.set(sourceId, effectId);
			return { effectId, repeat: false };
		},
		retire(sourceId) {
			held.delete(sourceId);
		},
	};
}

/** One reconcile pass this browser admitted, or read the receipt of. */
export interface AdmittedReconcilePass {
	sourceId: string;
	/** The effect identity the pass committed under — the pass's name. */
	effectId: string;
	/** The job the pass handed to the source's sync engine; empty when none. */
	jobId: string;
	status: SourceReconcileStatus;
	/** True when this receipt was read back rather than produced by this press. */
	repeat: boolean;
	/** When this browser learned the outcome, as an ISO instant. */
	at: string;
}

type Listener = (pass: AdmittedReconcilePass) => void;

// Keyed on `globalThis` for the same reason `sync-requests.ts` is: a solution
// remote that bundles its own copy of this kit must still hear the host
// panel's admissions, and the host's own schedules panel is a different module
// graph from the kit's.
const listenerKey = Symbol.for("codefly.saas-ui.source-reconcile-admitted");

function listeners(): Set<Listener> {
	const scope = globalThis as unknown as Record<
		symbol,
		Set<Listener> | undefined
	>;
	let set = scope[listenerKey];
	if (!set) {
		set = new Set<Listener>();
		scope[listenerKey] = set;
	}
	return set;
}

/**
 * Calls `listener` with every reconcile pass admitted from now on; returns the
 * unsubscribe. The host's reconcile-schedules panel is the listener this
 * exists for: it shows a person's pass beside the declared schedule's
 * occurrences, as the same admission of the same operation.
 */
export function onSourceReconcileAdmitted(listener: Listener): () => void {
	const set = listeners();
	set.add(listener);
	return () => {
		set.delete(listener);
	};
}

/** Announces one admitted pass. A throwing listener never stops the others. */
export function notifySourceReconcileAdmitted(
	pass: AdmittedReconcilePass,
): void {
	if (!pass.sourceId || !pass.effectId) return;
	for (const listener of [...listeners()]) {
		try {
			listener(pass);
		} catch {
			// A subscriber's failure is its own; the pass is already admitted.
		}
	}
}

/**
 * A fresh effect identity. `crypto.randomUUID` is the identity source wherever
 * it exists — it is in every browser this kit supports and in Node 19+ — and
 * the fallback is NOT a weaker uuid but a value that is still unique per press:
 * a collision would merge two people's passes into one receipt, so the fallback
 * carries the time as well as two random draws rather than pretending to be
 * random enough on its own.
 */
function mintEffectId(): string {
	const web = globalThis.crypto;
	if (web && typeof web.randomUUID === "function") return web.randomUUID();
	return [
		"pass",
		Date.now().toString(36),
		Math.random().toString(36).slice(2, 10),
		Math.random().toString(36).slice(2, 10),
	].join("-");
}
