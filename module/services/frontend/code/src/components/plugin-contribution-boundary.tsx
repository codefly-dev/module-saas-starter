"use client";

import { usePluginRuntime } from "@codefly/ui/plugin-host/runtime";
import {
	PluginErrorBoundary,
	type PluginFailure,
} from "@codefly/ui/plugin-host/ui";
import { AlertTriangle, CloudOff, RefreshCcw, Wrench } from "lucide-react";
import { type ReactNode, Suspense, use } from "react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

type PluginContributionKind = "route" | "widget";

interface PluginContributionBoundaryProps {
	plugin: string;
	contributionId: string;
	kind: PluginContributionKind;
	services?: readonly PluginContributionService[];
	children: ReactNode;
}

interface PluginContributionService {
	alias: string;
}

const stateCopy = {
	unavailable: {
		title: "Service temporarily unavailable",
		description: "This extension cannot reach its service right now.",
		icon: CloudOff,
	},
	incompatible: {
		title: "Extension update required",
		description:
			"This extension is not compatible with the current application.",
		icon: Wrench,
	},
	failed: {
		title: "Extension failed to load",
		description: "This extension encountered an unexpected problem.",
		icon: AlertTriangle,
	},
} as const;

function PluginLoadingState({ kind }: { kind: PluginContributionKind }) {
	return (
		<div
			role="status"
			aria-label={`Loading extension ${kind}`}
			data-plugin-state="loading"
			className={cn("space-y-3", kind === "route" && "py-2")}
		>
			<Skeleton className={kind === "route" ? "h-64 w-full" : "h-32 w-full"} />
		</div>
	);
}

function PluginFailureState({
	failure,
	kind,
	retry,
}: {
	failure: PluginFailure;
	kind: PluginContributionKind;
	retry(): void;
}) {
	const copy = stateCopy[failure.state as keyof typeof stateCopy];
	const Icon = copy.icon;
	return (
		<div
			role="alert"
			data-plugin-state={failure.state}
			data-plugin-error-code={failure.code}
			className={cn(
				"flex rounded-xl border border-border bg-card text-card-foreground",
				kind === "route"
					? "min-h-64 flex-col items-center justify-center gap-4 p-8 text-center"
					: "items-start gap-3 p-5",
			)}
		>
			<div className="flex size-10 shrink-0 items-center justify-center rounded-full bg-muted">
				<Icon className="size-5 text-muted-foreground" aria-hidden />
			</div>
			<div className={cn("space-y-1", kind === "widget" && "flex-1")}>
				<p className="font-medium">{copy.title}</p>
				<p className="text-sm text-muted-foreground">{copy.description}</p>
				{failure.requestId ? (
					<p className="pt-1 text-xs text-muted-foreground">
						Request ID: <code>{failure.requestId}</code>
					</p>
				) : null}
			</div>
			<Button type="button" variant="outline" size="sm" onClick={retry}>
				<RefreshCcw className="size-4" aria-hidden />
				Try again
			</Button>
		</div>
	);
}

function PluginCapabilityGate({
	plugin,
	services,
	children,
}: {
	plugin: string;
	services: readonly PluginContributionService[];
	children: ReactNode;
}) {
	const runtime = usePluginRuntime();
	for (const service of services)
		use(runtime.service(plugin, service.alias).capabilities());
	return children;
}

export function PluginContributionBoundary({
	plugin,
	contributionId,
	kind,
	services = [],
	children,
}: PluginContributionBoundaryProps) {
	return (
		<div
			data-plugin={plugin}
			data-plugin-contribution={contributionId}
			data-plugin-contribution-kind={kind}
		>
			<PluginErrorBoundary
				fallback={({ failure, retry }) => (
					<PluginFailureState failure={failure} kind={kind} retry={retry} />
				)}
			>
				<Suspense fallback={<PluginLoadingState kind={kind} />}>
					{services.length ? (
						<PluginCapabilityGate plugin={plugin} services={services}>
							{children}
						</PluginCapabilityGate>
					) : (
						children
					)}
				</Suspense>
			</PluginErrorBoundary>
		</div>
	);
}
