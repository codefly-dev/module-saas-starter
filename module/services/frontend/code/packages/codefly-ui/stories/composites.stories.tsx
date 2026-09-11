import { useState } from "react";
import { Chat, type ChatMessage } from "../src/chat/index.js";
import { Dashboard, type WidgetVisualization } from "../src/dashboard/index.js";
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
