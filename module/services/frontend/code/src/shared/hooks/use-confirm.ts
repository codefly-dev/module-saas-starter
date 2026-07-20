"use client";

import { useCallback, useState } from "react";

interface ConfirmState {
	isOpen: boolean;
	title: string;
	description: string;
	onConfirm: () => void;
}

export function useConfirm() {
	const [state, setState] = useState<ConfirmState>({
		isOpen: false,
		title: "",
		description: "",
		onConfirm: () => {},
	});

	const confirm = useCallback(
		({ title, description }: { title: string; description: string }) =>
			new Promise<boolean>((resolve) => {
				setState({
					isOpen: true,
					title,
					description,
					onConfirm: () => {
						resolve(true);
						setState((s) => ({ ...s, isOpen: false }));
					},
				});
			}),
		[],
	);

	const cancel = useCallback(() => {
		setState((s) => ({ ...s, isOpen: false }));
	}, []);

	return { ...state, confirm, cancel };
}
