"use client";

import type { ReactNode } from "react";
import { JsonView, TextBlock } from "../content/index.js";
import {
	Badge,
	Button,
	Card,
	Disclosure,
	Grid,
	Layout,
	Page,
	PageHeader,
	Section,
	Sidebar,
	SidebarContent,
	SidebarFooter,
	SidebarGroup,
	SidebarGroupContent,
	SidebarGroupLabel,
	SidebarHeader,
	SidebarInset,
	SidebarMenu,
	SidebarMenuButton,
	SidebarMenuItem,
	SidebarProvider,
	SidebarTrigger,
	Stack,
	useSidebar,
} from "../layout/index.js";

export interface PlaygroundScenario {
	id: string;
	label: string;
	description?: string;
	icon?: ReactNode;
}

export interface PlaygroundShellProps {
	title: string;
	moduleName: string;
	composition: string;
	scenarios: readonly PlaygroundScenario[];
	active: string;
	onSelect: (id: string) => void;
	actions?: ReactNode;
	notices?: ReactNode;
	children: ReactNode;
}

function Navigation({
	moduleName,
	composition,
	scenarios,
	active,
	onSelect,
}: PlaygroundShellProps) {
	const { setOpenMobile } = useSidebar();
	return (
		<Sidebar variant="inset">
			<SidebarHeader>
				<span className="type-card-title">{moduleName}</span>
			</SidebarHeader>
			<SidebarContent>
				<SidebarGroup>
					<SidebarGroupLabel>Explore capabilities</SidebarGroupLabel>
					<SidebarGroupContent>
						<nav aria-label="Playground scenarios">
							<SidebarMenu>
								{scenarios.map((scenario) => (
									<SidebarMenuItem key={scenario.id}>
										<SidebarMenuButton
											isActive={active === scenario.id}
											aria-current={active === scenario.id ? "page" : undefined}
											onClick={() => {
												onSelect(scenario.id);
												setOpenMobile(false);
											}}
										>
											{scenario.icon}
											<span>{scenario.label}</span>
										</SidebarMenuButton>
									</SidebarMenuItem>
								))}
							</SidebarMenu>
						</nav>
					</SidebarGroupContent>
				</SidebarGroup>
			</SidebarContent>
			<SidebarFooter>
				<footer className="type-caption-plain text-muted-foreground">
					Running in {composition}
				</footer>
			</SidebarFooter>
		</Sidebar>
	);
}

function toggleTheme() {
	const root = document.documentElement;
	// Preserve the preview's pre-render palette on mount; only a click changes it.
	// Suppress transitions while foreground and background change together.
	const style = document.createElement("style");
	style.textContent = "*,*::before,*::after{transition:none!important}";
	document.head.appendChild(style);
	root.style.colorScheme = root.classList.toggle("dark") ? "dark" : "light";
	void getComputedStyle(root).color;
	requestAnimationFrame(() => requestAnimationFrame(() => style.remove()));
}

/** The shared frame. The page supplies navigation state, content and operations. */
export function PlaygroundShell(props: PlaygroundShellProps) {
	const scenario = props.scenarios.find((item) => item.id === props.active);
	return (
		<SidebarProvider>
			<Navigation {...props} />
			<SidebarInset className="min-w-0">
				<Layout className="p-6">
					<Page>
						<Stack direction="row" justify="between" align="center">
							<SidebarTrigger />
							<Stack
								direction="row"
								gap={2}
								align="center"
								className="min-w-0 flex-wrap justify-end"
							>
								{props.actions}
								<Button variant="ghost" onClick={toggleTheme}>
									<span className="dark:hidden">Dark mode</span>
									<span className="hidden dark:inline">Light mode</span>
								</Button>
							</Stack>
						</Stack>
						<PageHeader
							title={props.title}
							description={scenario?.description}
						/>
						{props.notices}
						{props.children}
					</Page>
				</Layout>
			</SidebarInset>
		</SidebarProvider>
	);
}

export type PlaygroundCapability = {
	id: string;
	title: string;
	description: string;
	action?: ReactNode;
} & (
	| { status: "demonstrated"; reason?: string }
	| { status: "reachable" | "unreachable"; reason: string }
);

const capabilityStatus = {
	demonstrated: { label: "Demonstrated", tone: "success" },
	reachable: { label: "Reachable, not demonstrated", tone: "warning" },
	unreachable: { label: "Not reachable", tone: "danger" },
} as const;

export function CapabilityGuide({
	capabilities,
}: {
	capabilities: readonly PlaygroundCapability[];
}) {
	return (
		<Section title="Public capabilities">
			{capabilities.length === 0 ? (
				<TextBlock text="No capabilities supplied." />
			) : (
				<Grid cols={2}>
					{capabilities.map((capability) => (
						<Card
							key={capability.id}
							title={capability.title}
							description={capability.description}
						>
							<Stack gap={2}>
								<Badge tone={capabilityStatus[capability.status].tone}>
									{capabilityStatus[capability.status].label}
								</Badge>
								{capability.reason && <TextBlock text={capability.reason} />}
								{capability.action}
							</Stack>
						</Card>
					))}
				</Grid>
			)}
		</Section>
	);
}

export type OperationResult =
	| { state: "pending" | "running" | "done"; outcome?: string; refusal?: never }
	| { state: "failed"; refusal: string; outcome?: never };

export type PlaygroundOperation = {
	/** Stable call identity; repeated calls to one operation keep separate entries. */
	id: string;
	operation: string;
	request: unknown;
	response?: unknown;
} & OperationResult;

const operationStatus = {
	pending: { label: "Pending", tone: "neutral" },
	running: { label: "Running", tone: "info" },
	done: { label: "Done", tone: "success" },
	failed: { label: "Failed", tone: "danger" },
} as const;

function Outcome({ result }: { result: OperationResult }) {
	return (
		<Stack gap={2}>
			<Badge tone={operationStatus[result.state].tone}>
				{operationStatus[result.state].label}
			</Badge>
			{result.state === "failed" ? (
				<TextBlock text={result.refusal} />
			) : (
				result.outcome && <TextBlock text={result.outcome} />
			)}
		</Stack>
	);
}

function Exchange({
	operation,
	request,
	response,
}: Pick<PlaygroundOperation, "operation" | "request" | "response">) {
	return (
		<Grid cols={2}>
			<Stack gap={2}>
				<span className="type-caption-plain">Request</span>
				<JsonView label={`${operation} request`} value={request} />
			</Stack>
			<Stack gap={2}>
				<span className="type-caption-plain">Response</span>
				{response === undefined ? (
					<TextBlock text="No response yet." />
				) : (
					<JsonView label={`${operation} response`} value={response} />
				)}
			</Stack>
		</Grid>
	);
}

export function ScenarioSteps({
	title = "Scenario steps",
	children,
}: {
	title?: string;
	children: ReactNode;
}) {
	return (
		<Section title={title}>
			<ol className="list-decimal space-y-4 pl-6">{children}</ol>
		</Section>
	);
}

export type StepProps = Omit<PlaygroundOperation, "id"> &
	OperationResult & {
		title: string;
		children?: ReactNode;
	};

export function Step(props: StepProps) {
	return (
		<li>
			<Card title={props.title} description={props.operation}>
				<Stack>
					<Outcome result={props} />
					<Exchange {...props} />
					{props.children}
				</Stack>
			</Card>
		</li>
	);
}

/** Append calls in invocation order; this component neither sorts nor executes them. */
export function OperationLog({
	entries,
}: {
	entries: readonly PlaygroundOperation[];
}) {
	return (
		<Section title="Operation log">
			{entries.length === 0 ? (
				<TextBlock text="No operations called yet." />
			) : (
				<ol className="list-decimal space-y-4 pl-6">
					{entries.map((entry) => (
						<li key={entry.id}>
							<Disclosure
								title={
									<>
										{entry.operation}
										<Badge tone={operationStatus[entry.state].tone}>
											{operationStatus[entry.state].label}
										</Badge>
									</>
								}
							>
								<Stack>
									<Outcome result={entry} />
									<Exchange {...entry} />
								</Stack>
							</Disclosure>
						</li>
					))}
				</ol>
			)}
		</Section>
	);
}
