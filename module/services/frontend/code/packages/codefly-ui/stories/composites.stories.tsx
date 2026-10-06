import { useState } from "react";
import { Chat, type ChatMessage } from "../src/chat/index.js";
import {
	Dashboard,
	SortableBoard,
	SortableGrid,
	type WidgetVisualization,
} from "../src/dashboard/index.js";
import { Card } from "../src/layout/index.js";
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
