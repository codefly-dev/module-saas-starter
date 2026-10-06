export type DisposableMount = { dispose(): void };

/** A mount owns one child, including while its asynchronous setup is pending. */
export function mountIsolated<T extends DisposableMount>(
	host: HTMLElement,
	mount: (element: HTMLElement, signal: AbortSignal) => Promise<T>,
	callbacks: {
		onReady(handle: T): void;
		onError(error: unknown, retired: boolean): void;
	},
): { retire(): void } {
	const element = host.ownerDocument.createElement("div");
	element.style.width = "100%";
	element.style.height = "100%";
	host.appendChild(element);
	const controller = new AbortController();
	let retired = false;
	let handle: T | undefined;
	const dispose = () => {
		const owned = handle;
		handle = undefined;
		if (owned) {
			try {
				owned.dispose();
			} catch (error) {
				callbacks.onError(error, retired);
			}
		}
	};
	const retire = () => {
		if (retired) return;
		retired = true;
		controller.abort();
		element.remove();
		dispose();
	};
	// Deferral also turns synchronous setup throws into the same rejection path.
	void Promise.resolve()
		.then(() => mount(element, controller.signal))
		.then(
			(owned) => {
				handle = owned;
				if (retired) dispose();
				else {
					try {
						callbacks.onReady(owned);
					} catch (error) {
						const wasRetired = retired;
						retire();
						callbacks.onError(error, wasRetired);
					}
				}
			},
			(error: unknown) => {
				element.remove();
				callbacks.onError(error, retired);
			},
		);
	return { retire };
}
