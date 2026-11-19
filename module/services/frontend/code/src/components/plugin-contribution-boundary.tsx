"use client";

import { usePluginRuntime } from "@codefly-dev/ui/plugin-host/runtime";
import {
	PluginErrorBoundary,
	type PluginFailure,
} from "@codefly-dev/ui/plugin-host/ui";
import { AlertTriangle, CloudOff, RefreshCcw, Wrench } from "lucide-react";
import { type ReactNode, Suspense, use } from "react";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { Button, ErrorState, Page, PageHeader, Panel } from "@/shared/ui";

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
			"Ask your administrator to update this extension to match the application.",
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
	const denied =
		failure.code === "http_403" || failure.code === "permission_denied";
	const content = (
		<Panel
			data-plugin-state={failure.state}
			data-plugin-error-code={failure.code}
			className="space-y-4"
		>
			<ErrorState
				icon={<Icon aria-hidden />}
				title={denied ? "Access is unavailable" : copy.title}
				detail={
					denied
						? "You don't have access to this page. Ask your administrator to grant access."
						: copy.description
				}
			/>
			{failure.requestId ? (
				<p className="text-xs text-muted-foreground">
					Support reference: <code>{failure.requestId}</code>
				</p>
			) : null}
			{!denied && failure.state !== "incompatible" && (
				<Button type="button" variant="outline" size="sm" onClick={retry}>
					<RefreshCcw className="size-4" aria-hidden />
					Try again
				</Button>
			)}
		</Panel>
	);
	return kind === "route" ? (
		<Page>
			<PageHeader title="Page unavailable" />
			{content}
		</Page>
	) : (
		content
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
