import { useState } from "react";
import { Chat, type ChatMessage } from "../src/chat/index.js";
import {
	Dashboard,
	DashboardSectionHeader,
	SortableBoard,
	SortableGrid,
	type WidgetVisualization,
} from "../src/dashboard/index.js";
import { Button, Card } from "../src/layout/index.js";
export default { title: "Shared UI/Composites" };
export const Conversation = {
	render: function ConversationStory() {
		const [messages, setMessages] = useState<ChatMessage[]>([
			{
				id: "intro",
				role: "assistant",
				content: "How can I help with this workspace?",
			},
		]);
		return (
			<Chat
				title="Example conversation"
				messages={messages}
				onSend={(content) =>
					setMessages((previous) => [
						...previous,
						{ id: String(previous.length), role: "user", content },
					])
				}
			/>
		);
	},
};
export const EmptyConversation = {
	render: () => <Chat messages={[]} title="Example conversation" />,
};
export const StreamingConversation = {
	render: () => (
		<Chat
			messages={[
				{
					id: "pending",
					role: "assistant",
					content: "Preparing an example response…",
					pending: true,
				},
			]}
			busy
		/>
	),
};
export const DashboardCharts = {
	render: () => (
		<Dashboard
			data={{
				title: "Example dashboard",
				widgets: (
					[
						"line",
						"area",
						"bar",
						"number",
						"table",
					] satisfies WidgetVisualization[]
				).map((visualization) => ({
					id: visualization,
					title: visualization,
					visualization,
					series: {
						points: [
							{ key: "Mon", value: 3 },
							{ key: "Tue", value: 7 },
						],
						total: 10,
					},
				})),
			}}
		/>
	),
};
// A dashboard in two declared sections, and one section no widget names. A
// widget inside a section is titled one level under the section's heading.
const days = [
	{ key: "2026-09-01T00:00:00+00", value: 3 },
	{ key: "2026-09-02T00:00:00+00", value: 0 },
	{ key: "2026-09-03T00:00:00+00", value: 7 },
];
export const DashboardSections = {
	render: () => (
		<Dashboard
			data={{
				title: "Example dashboard",
				sections: [
					{ id: "overview", title: "Overview", description: "All time" },
					{ id: "trends", title: "Trends" },
					{ id: "later", title: "Later" },
				],
				widgets: [
					{
						id: "total",
						title: "Sign-ins",
						visualization: "number",
						section: "overview",
						series: { points: days, total: 10 },
					},
					{
						id: "rate",
						title: "Success rate",
						visualization: "number",
						section: "overview",
						format: "percent",
						series: { points: [{ key: "all", value: 40 }], total: 40 },
					},
					{
						id: "per_day",
						title: "Sign-ins per day",
						visualization: "line",
						section: "trends",
						series: { points: days, total: 10 },
					},
				],
			}}
		/>
	),
};

// The heading a host draws over a section of its own layout: a handle before
// the title, controls after it, and a note under it.
export const SectionHeader = {
	render: () => (
		<DashboardSectionHeader
			title="Overview"
			description="The headline numbers."
			leading={<span aria-hidden>⋮⋮</span>}
			actions={
				<Button type="button" variant="outline" size="sm">
					Add a widget
				</Button>
			}
		>
			<p className="type-body text-muted-foreground">
				No widgets in this section.
			</p>
		</DashboardSectionHeader>
	),
};

export const EmptyDashboard = {
	render: () => (
		<Dashboard data={{ title: "Example dashboard", widgets: [] }} />
	),
};

// Drag a tile onto another to swap the two.
function SortableTilesExample() {
	const [ids, setIds] = useState(["Requests", "Errors", "Latency", "Users"]);
	return (
		<SortableGrid
			ids={ids}
			onSwap={(dragged, target) =>
				setIds((current) =>
					current.map((id) =>
						id === dragged ? target : id === target ? dragged : id,
					),
				)
			}
			className="grid grid-cols-2 gap-4"
			renderItem={(id) => (
				<Card>
					<p>{id}</p>
				</Card>
			)}
		/>
	);
}
export const SortableTiles = {
	render: () => <SortableTilesExample />,
};

// Drag a tile onto another to swap the two, across groups too, or onto the
// slot at the end of a group to move it there. Drag a group by the grip in its
// header to put it before or after another.
function SortableGroupsExample() {
	const [groups, setGroups] = useState([
		{ id: "overview", title: "Overview", ids: ["Requests", "Users"] },
		{ id: "detail", title: "Detail", ids: ["Errors", "Latency"] },
	]);
	return (
		<SortableBoard
			className="space-y-6"
			groups={groups.map((group) => ({
				id: group.id,
				ids: group.ids,
				label: group.title,
				header: (handle) => (
					<p className="mb-3 flex items-center gap-2">
						<button
							type="button"
							aria-label={`Move ${group.title}`}
							className="cursor-grab"
							{...handle}
						>
							⠿
						</button>
						{group.title}
					</p>
				),
				preview: <p>{group.title}</p>,
				className: "grid grid-cols-2 gap-4",
			}))}
			onSwap={(dragged, target) =>
				setGroups((current) =>
					current.map((group) => ({
						...group,
						ids: group.ids.map((id) =>
							id === dragged ? target : id === target ? dragged : id,
						),
					})),
				)
			}
			onMove={(dragged, to) =>
				setGroups((current) =>
					current.map((group) => ({
						...group,
						ids: [
							...group.ids.filter((id) => id !== dragged),
							...(group.id === to ? [dragged] : []),
						],
					})),
				)
			}
			onMoveGroup={(moved, index) =>
				setGroups((current) => {
					const next = current.filter((group) => group.id !== moved);
					const group = current.find(({ id }) => id === moved);
					if (group) next.splice(index, 0, group);
					return next;
				})
			}
			renderItem={(id) => (
				<Card>
					<p>{id}</p>
				</Card>
			)}
		/>
	);
}
export const SortableGroups = {
	render: () => <SortableGroupsExample />,
};
