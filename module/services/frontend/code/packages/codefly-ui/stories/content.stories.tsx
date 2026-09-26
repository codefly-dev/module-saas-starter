import {
	CodeBlock,
	Content,
	JsonView,
	Markdown,
} from "../src/content/index.js";

export default { title: "Shared UI/Content" };

const ANSWER = `## Quarterly summary

Revenue grew **12%** against the prior quarter, driven by the *Example Solution* rollout.
See the [methodology](https://example.com/methodology) for details.

### Open items

- [x] Reconcile the ledger export
- [ ] Confirm the regional split
- Follow up with \`user@example.com\`

| Region | Q1 | Q2 |
| :----- | -: | -: |
| North  | 1.2 | 1.4 |
| South  | 0.8 | 0.9 |

> Figures are unaudited until the close.

\`\`\`python
def growth(previous: float, current: float) -> float:
    # Relative change, as a fraction.
    return (current - previous) / previous
\`\`\`

Untrusted input stays inert: [a script link](javascript:alert(1)), <b onclick="alert(1)">raw HTML</b>,
and an image that is never fetched: ![remote chart](https://example.com/chart.png).`;

const PAYLOAD = {
	eventId: "7f0c2a4e-5b1d-4c3e-9a8f-2d6b1e0c9a77",
	actor: {
		id: "user-123",
		email: "jane.doe@example.com",
		roles: ["admin", "auditor"],
	},
	resource: { type: "document", id: "doc-42", path: "/reports/2026/q2.md" },
	changes: [
		{ field: "title", from: "Draft", to: "Q2 report" },
		{ field: "status", from: "draft", to: "published" },
	],
	approved: true,
	retries: 0,
	note: null,
	rows: Array.from({ length: 250 }, (_, index) => ({
		index,
		value: index * 1.5,
	})),
};

export const MarkdownAnswer = {
	render: () => (
		<div className="max-w-2xl p-4">
			<Content value={ANSWER} />
		</div>
	),
};

export const JsonPayload = {
	render: () => (
		<div className="max-w-2xl p-4">
			<JsonView value={PAYLOAD} label="Audit payload" expandDepth={2} />
		</div>
	),
};

export const HighlightedCode = {
	render: () => (
		<div className="max-w-2xl p-4">
			<CodeBlock
				language="ts"
				code={`import { Content } from "@codefly-dev/ui/content";

// One component for every kind of content.
export function Answer({ text }: { text: string }) {
	return <Content value={text} format="markdown" />;
}`}
			/>
		</div>
	),
};

export const PlainText = {
	render: () => (
		<div className="max-w-2xl p-4">
			<Content
				value={
					"Line one\n    indented line two\n\nA paragraph after a blank line."
				}
				format="text"
			/>
		</div>
	),
};

export const InlineExcerpts = {
	render: () => (
		<ul className="max-w-sm space-y-2 p-4">
			<li>
				<Content value={ANSWER} variant="inline" />
			</li>
			<li>
				<Content value={PAYLOAD} variant="inline" />
			</li>
			<li>
				<Content
					value={
						"A citation excerpt that wraps onto a second line before it is cut short with an ellipsis, as a search result would show it."
					}
					variant="inline"
					lines={2}
				/>
			</li>
		</ul>
	),
};

export const DocumentPreview = {
	render: () => (
		<div className="max-w-2xl p-4">
			<Markdown headingLevel={2}>
				{
					"# Onboarding guide\n\n## Access\n\nRequest access from your administrator.\n\n## First steps\n\n1. Sign in\n2. Choose a workspace"
				}
			</Markdown>
		</div>
	),
};
