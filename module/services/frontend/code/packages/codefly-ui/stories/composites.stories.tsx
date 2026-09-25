import { useState } from "react";
import { Chat, type ChatMessage } from "../src/chat/index.js";
import {
	Dashboard,
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
