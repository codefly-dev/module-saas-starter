"use client";

import { Children, type ReactNode } from "react";
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

const defaultLabels = {
	navigation: "Playground scenarios",
	navigationTitle: "Explore capabilities",
	toggleNavigation: "Toggle Sidebar",
	emptyScenarios: "No scenarios supplied.",
	composition: "Running in",
	lightMode: "Light mode",
	darkMode: "Dark mode",
	demonstrated: "Demonstrated",
	reachable: "Reachable, not demonstrated",
	unreachable: "Not reachable",
	pending: "Pending",
	running: "Running",
	done: "Done",
	failed: "Failed",
	request: "Request",
	response: "Response",
	noResponse: "No response yet.",
};
export type PlaygroundLabels = Partial<typeof defaultLabels>;
type EvidenceSectionProps = {
	title?: ReactNode;
	emptyState?: ReactNode;
};

export interface PlaygroundThemeToggleProps {
	mode: "light" | "dark";
	onModeChange: (mode: "light" | "dark") => void;
	labels?: PlaygroundLabels;
}

/** Presentation only: the caller owns the mode and applies the requested change. */
export function PlaygroundThemeToggle({
	mode,
	onModeChange,
	labels,
}: PlaygroundThemeToggleProps) {
	const text = { ...defaultLabels, ...labels };
	return (
		<Button
			variant="ghost"
			onClick={() => onModeChange(mode === "dark" ? "light" : "dark")}
		>
			{mode === "dark" ? text.lightMode : text.darkMode}
		</Button>
	);
}

export interface PlaygroundScenario {
	id: string;
	label: string;
	description?: string;
	icon?: ReactNode;
}

export interface PlaygroundShellProps {
	title: string;
	description?: string;
	/** Layout gutters; use p-0 inside an already padded host shell. */
	className?: string;
	theme?: PlaygroundThemeToggleProps;
	labels?: PlaygroundLabels;
	moduleName: string;
	composition: string;
	scenarios: readonly PlaygroundScenario[];
	active: string;
	onSelect: (id: string) => void;
	/** Caller-owned controls, including theme changes through its own provider. */
	actions?: ReactNode;
	notices?: ReactNode;
	children: ReactNode;
}

function Navigation({
	moduleName,
	labels,
	scenarios,
	active,
	onSelect,
}: PlaygroundShellProps) {
	const { setOpenMobile } = useSidebar();
	const text = { ...defaultLabels, ...labels };
	return (
		<Sidebar variant="inset">
			<SidebarHeader>
				<span className="type-card-title [overflow-wrap:anywhere]">
					{moduleName}
				</span>
			</SidebarHeader>
			<SidebarContent>
				<SidebarGroup>
					<SidebarGroupLabel>{text.navigationTitle}</SidebarGroupLabel>
					<SidebarGroupContent>
						<nav aria-label={text.navigation}>
							{scenarios.length === 0 && (
								<TextBlock text={text.emptyScenarios} />
							)}
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
		</Sidebar>
	);
}

/** The shared frame. The page supplies navigation state, content and operations. */
export function PlaygroundShell(props: PlaygroundShellProps) {
	const text = { ...defaultLabels, ...props.labels };
	const scenario = props.scenarios.find((item) => item.id === props.active);
	return (
		<SidebarProvider>
			<Navigation {...props} />
			<SidebarInset className="min-w-0">
				<Layout className={props.className ?? "p-6"}>
					<Page>
						<Stack direction="row" justify="between" align="center">
							<SidebarTrigger aria-label={text.toggleNavigation} />
							<Stack
								direction="row"
								gap={2}
								align="center"
								className="min-w-0 flex-wrap justify-end"
							>
								{props.actions}
								{props.theme && (
									<PlaygroundThemeToggle
										{...props.theme}
										labels={props.theme.labels ?? props.labels}
									/>
								)}
							</Stack>
						</Stack>
						<PageHeader
							className="[overflow-wrap:anywhere]"
							title={props.title}
							description={
								scenario?.description ??
								props.description ??
								(props.scenarios.length === 0 ? text.emptyScenarios : undefined)
							}
						/>
						{props.notices}
						{props.children}
						<p className="type-caption-plain text-muted-foreground [overflow-wrap:anywhere]">
							{text.composition} {props.composition}
						</p>
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
	demonstrated: { tone: "success" },
	reachable: { tone: "warning" },
	unreachable: { tone: "danger" },
} as const;

export function CapabilityGuide({
	capabilities,
	title = "Public capabilities",
	emptyState = "No capabilities supplied.",
	labels,
}: EvidenceSectionProps & {
	capabilities: readonly PlaygroundCapability[];
	labels?: PlaygroundLabels;
}) {
	const text = { ...defaultLabels, ...labels };
	return (
		<Section title={title}>
			{capabilities.length === 0 ? (
				emptyState
			) : (
				<Grid cols={2}>
					{capabilities.map((capability) => (
						<Card
							key={capability.id}
							className="[overflow-wrap:anywhere]"
							title={capability.title}
							description={capability.description}
						>
							<Stack gap={2}>
								<Badge tone={capabilityStatus[capability.status].tone}>
									{text[capability.status]}
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
	pending: { tone: "neutral" },
	running: { tone: "info" },
	done: { tone: "success" },
	failed: { tone: "danger" },
} as const;

function Outcome({ result }: { result: OperationResult }) {
	const message = result.state === "failed" ? result.refusal : result.outcome;
	return message ? <TextBlock text={message} /> : null;
}

function Exchange({
	operation,
	request,
	response,
	labels,
}: Pick<PlaygroundOperation, "operation" | "request" | "response"> & {
	labels?: PlaygroundLabels;
}) {
	const text = { ...defaultLabels, ...labels };
	return (
		<Grid cols={2}>
			<Stack gap={2}>
				<span className="type-caption-plain">{text.request}</span>
				<JsonView
					label={`${operation} ${text.request.toLowerCase()}`}
					value={request}
				/>
			</Stack>
			<Stack gap={2}>
				<span className="type-caption-plain">{text.response}</span>
				{response === undefined ? (
					<TextBlock text={text.noResponse} />
				) : (
					<JsonView
						label={`${operation} ${text.response.toLowerCase()}`}
						value={response}
					/>
				)}
			</Stack>
		</Grid>
	);
}

export function ScenarioSteps({
	title = "Scenario steps",
	emptyState = "No steps supplied.",
	children,
}: EvidenceSectionProps & {
	children?: ReactNode;
}) {
	return (
		<Section title={title}>
			{Children.toArray(children).length === 0 ? (
				emptyState
			) : (
				<ol className="list-decimal space-y-4 pl-6">{children}</ol>
			)}
		</Section>
	);
}

export type StepProps = Omit<PlaygroundOperation, "id"> &
	OperationResult & {
		title: string;
		labels?: PlaygroundLabels;
		children?: ReactNode;
	};

export function Step(props: StepProps) {
	const text = { ...defaultLabels, ...props.labels };
	return (
		<li>
			<Card
				className="[overflow-wrap:anywhere]"
				title={props.title}
				description={props.operation}
			>
				<Stack>
					<Badge tone={operationStatus[props.state].tone}>
						{text[props.state]}
					</Badge>
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
	title = "Operation log",
	emptyState = "No operations called yet.",
	labels,
}: EvidenceSectionProps & {
	entries: readonly PlaygroundOperation[];
	labels?: PlaygroundLabels;
}) {
	const text = { ...defaultLabels, ...labels };
	return (
		<Section title={title}>
			{entries.length === 0 ? (
				emptyState
			) : (
				<ol className="list-decimal space-y-4 pl-6">
					{entries.map((entry) => (
						<li key={entry.id}>
							<Disclosure
								className="[overflow-wrap:anywhere]"
								title={
									<>
										{entry.operation}
										<Badge tone={operationStatus[entry.state].tone}>
											{text[entry.state]}
										</Badge>
									</>
								}
							>
								<Stack>
									<Outcome result={entry} />
									<Exchange {...entry} labels={labels} />
								</Stack>
							</Disclosure>
						</li>
					))}
				</ol>
			)}
		</Section>
	);
}
