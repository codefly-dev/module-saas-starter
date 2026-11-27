"use client";

import { PanelLeft, PanelsTopLeft } from "lucide-react";
import { Children, type ReactNode, useId, useState } from "react";
import { JsonView, TextBlock } from "../content/index.js";
import {
	Badge,
	Button,
	Card,
	Disclosure,
	Grid,
	Layout,
	Section,
	Stack,
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
	dividerBefore?: boolean;
}

export interface PlaygroundShellProps {
	title: string;
	description?: string;
	/** Layout gutters; use p-0 inside an already padded host shell. */
	className?: string;
	theme?: PlaygroundThemeToggleProps;
	labels?: PlaygroundLabels;
	moduleName: string;
	moduleIcon?: ReactNode;
	brandSuffix?: string;
	navigationStatus?: ReactNode;
	/** Retained panels render their own shared header beside panel-specific actions. */
	panelHeaders?: boolean;
	/** For retained tab panels, provide their DOM ids to enable tab semantics and arrow navigation. */
	panelId?: (scenarioId: string) => string;
	composition: string;
	scenarios: readonly PlaygroundScenario[];
	active: string;
	onSelect: (id: string) => void;
	/** Caller-owned controls, including theme changes through its own provider. */
	actions?: ReactNode;
	notices?: ReactNode;
	children: ReactNode;
}

export function PlaygroundPanelHeader({
	title,
	description,
	icon,
	children,
}: {
	title: string;
	description?: string;
	icon?: ReactNode;
	children?: ReactNode;
}) {
	return (
		<header className="pg-panel-head">
			{icon && <div className="pg-panel-icon">{icon}</div>}
			<div>
				<h1>{title}</h1>
				{description && <p>{description}</p>}
			</div>
			{children}
		</header>
	);
}

export function PlaygroundShell(props: PlaygroundShellProps) {
	const text = { ...defaultLabels, ...props.labels };
	const { scenarios, active, panelId } = props;
	const [navigationOpen, setNavigationOpen] = useState(true);
	const navigationId = useId();
	const selected = scenarios.find((item) => item.id === active);
	return (
		<div className="pg">
			<header className="pg-top">
				<div className="pg-brand">
					<span>{props.moduleIcon ?? <PanelsTopLeft size={21} />}</span>
					<div>
						{props.moduleName}
						{props.brandSuffix && <strong>{props.brandSuffix}</strong>}
					</div>
				</div>
				<div className="pg-top-right">
					<button
						type="button"
						className="pg-icon"
						aria-label={text.toggleNavigation}
						aria-controls={navigationId}
						aria-expanded={navigationOpen}
						onClick={() => setNavigationOpen((value) => !value)}
					>
						<PanelLeft size={16} />
					</button>
					<span className="pg-local">
						{text.composition} {props.composition}
					</span>
					{props.theme && (
						<PlaygroundThemeToggle
							{...props.theme}
							labels={props.theme.labels ?? props.labels}
						/>
					)}
					{props.actions}
				</div>
			</header>
			<div className="pg-workspace">
				<aside id={navigationId} className="pg-rail" hidden={!navigationOpen}>
					<div className="pg-rail-heading">{text.navigationTitle}</div>
					<nav aria-label={text.navigation}>
						{!scenarios.length && <TextBlock text={text.emptyScenarios} />}
						<div
							className="pg-nav-items"
							role={panelId ? "tablist" : undefined}
							aria-label={panelId ? text.navigation : undefined}
							aria-orientation={panelId ? "vertical" : undefined}
						>
							{scenarios.map((item, index) => (
								<button
									key={item.id}
									type="button"
									className={
										item.dividerBefore ? "pg-tab pg-divider" : "pg-tab"
									}
									role={panelId ? "tab" : undefined}
									aria-selected={panelId ? active === item.id : undefined}
									aria-current={
										!panelId && active === item.id ? "page" : undefined
									}
									aria-controls={panelId?.(item.id)}
									tabIndex={panelId && active !== item.id ? -1 : undefined}
									onKeyDown={
										panelId
											? (event) => {
													const offsets: Record<string, number> = {
														ArrowDown: 1,
														ArrowRight: 1,
														ArrowUp: -1,
														ArrowLeft: -1,
														Home: -index,
														End: scenarios.length - index - 1,
													};
													const offset = offsets[event.key];
													if (offset === undefined) return;
													event.preventDefault();
													const next =
														(index + offset + scenarios.length) %
														scenarios.length;
													props.onSelect(scenarios[next].id);
													event.currentTarget
														.closest('[role="tablist"]')
														?.querySelectorAll<HTMLElement>('[role="tab"]')
														[next]?.focus();
												}
											: undefined
									}
									onClick={() => props.onSelect(item.id)}
								>
									<span>{item.icon}</span>
									{item.label}
								</button>
							))}
						</div>
					</nav>
					{props.navigationStatus && (
						<div className="pg-rail-foot">{props.navigationStatus}</div>
					)}
				</aside>
				<main className="pg-main">
					{props.panelHeaders ? (
						<>
							{props.notices}
							{props.children}
						</>
					) : (
						<section className="pg-panel">
							<PlaygroundPanelHeader
								title={props.title}
								description={
									selected?.description ??
									props.description ??
									(!scenarios.length ? text.emptyScenarios : undefined)
								}
								icon={selected?.icon}
							/>
							<Layout className={props.className ?? "pg-content"}>
								{props.notices}
								{props.children}
							</Layout>
						</section>
					)}
				</main>
			</div>
		</div>
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
