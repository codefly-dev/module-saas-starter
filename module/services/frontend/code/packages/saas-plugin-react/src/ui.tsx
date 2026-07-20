"use client";

import { Component, type ReactNode } from "react";
import { toPluginFailure, type PluginFailure } from "./availability.js";

export type {
	PluginAvailabilityState,
	PluginFailure,
	PluginFailureState,
} from "./availability.js";

export interface PluginErrorBoundaryFallbackProps {
	failure: PluginFailure;
	retry(): void;
}

export interface PluginErrorBoundaryProps {
	children: ReactNode;
	fallback(props: PluginErrorBoundaryFallbackProps): ReactNode;
	onError?(failure: PluginFailure): void;
}

interface PluginErrorBoundaryState {
	failure: PluginFailure | null;
}

/** Contains one route or widget without prescribing host/product styling. */
export class PluginErrorBoundary extends Component<
	PluginErrorBoundaryProps,
	PluginErrorBoundaryState
> {
	state: PluginErrorBoundaryState = { failure: null };

	static getDerivedStateFromError(error: unknown): PluginErrorBoundaryState {
		return { failure: toPluginFailure(error) };
	}

	componentDidCatch(): void {
		if (this.state.failure) this.props.onError?.(this.state.failure);
	}

	private readonly retry = () => this.setState({ failure: null });

	render(): ReactNode {
		return this.state.failure
			? this.props.fallback({
					failure: this.state.failure,
					retry: this.retry,
				})
			: this.props.children;
	}
}
