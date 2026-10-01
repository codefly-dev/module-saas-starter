export type CapturedTask<C> = { readonly context: C };
export type TaskEntry<C, O> =
	| { readonly task: CapturedTask<C>; readonly state: "pending" }
	| {
			readonly task: CapturedTask<C>;
			readonly state: "settled";
			readonly outcome: O;
	  };

/**
 * Session-local ownership of captured work. The caller supplies an immutable
 * context before starting work, owns outcome resources, and forgets settled
 * entries after releasing those resources. Invalidating presentation never
 * cancels work or discards its outcome.
 */
export function createTaskTracker<C, O>() {
	const retained = new Map<CapturedTask<C>, TaskEntry<C, O>>();
	let current: CapturedTask<C> | undefined;
	return {
		begin(context: C): CapturedTask<C> {
			const task = Object.freeze({ context });
			retained.set(task, Object.freeze({ task, state: "pending" }));
			current = task;
			return task;
		},
		invalidate(): void {
			current = undefined;
		},
		isCurrent(task: CapturedTask<C>): boolean {
			return current === task;
		},
		settle(task: CapturedTask<C>, outcome: O): TaskEntry<C, O> {
			const entry = retained.get(task);
			if (!entry) throw new Error("This task does not belong to this session.");
			if (entry.state === "settled")
				throw new Error("This task already settled.");
			const next = Object.freeze({ task, state: "settled" as const, outcome });
			retained.set(task, next);
			return next;
		},
		entries({
			offset = 0,
			limit,
		}: {
			offset?: number;
			limit: number;
		}): readonly TaskEntry<C, O>[] {
			if (
				!Number.isSafeInteger(offset) ||
				offset < 0 ||
				!Number.isSafeInteger(limit) ||
				limit < 0
			) {
				throw new Error(
					"Entry offset and limit must be nonnegative safe integers.",
				);
			}
			const result: TaskEntry<C, O>[] = [];
			let index = 0;
			for (const entry of retained.values()) {
				if (result.length === limit) break;
				if (index++ >= offset) result.push(entry);
			}
			return result;
		},
		forget(task: CapturedTask<C>): void {
			const entry = retained.get(task);
			if (!entry) throw new Error("This task does not belong to this session.");
			if (entry.state === "pending")
				throw new Error(
					"Pending work must settle before its outcome can be released.",
				);
			retained.delete(task);
			if (current === task) current = undefined;
		},
	};
}
