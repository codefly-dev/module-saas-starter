import { useState } from "react";
import { Board, type BoardColumn } from "../src/board/index.js";
import { Badge } from "../src/layout/index.js";

export default { title: "Shared UI/Board" };

interface Request {
	ref: string;
	name: string;
	stage: string;
	owner: string;
	opened: string;
}

const COLUMNS: BoardColumn[] = [
	{ id: "open", label: "Open", empty: "Nothing waiting" },
	{ id: "review", label: "In review" },
	{ id: "done", label: "Done" },
	{ id: "refused", label: "Refused", empty: "Nothing refused" },
];

const REQUESTS: Request[] = [
	{
		ref: "r-1",
		name: "Widen the export window",
		stage: "open",
		owner: "Jane Doe",
		opened: "2 Oct",
	},
	{
		ref: "r-2",
		name: "Second approver for a refund",
		stage: "open",
		owner: "Sam Roe",
		opened: "3 Oct",
	},
	{
		ref: "r-3",
		name: "Retire the legacy webhook",
		stage: "review",
		owner: "Jane Doe",
		opened: "28 Sep",
	},
	{
		ref: "r-4",
		name: "Regional split in the ledger",
		stage: "done",
		owner: "Alex Poe",
		opened: "12 Sep",
	},
];

/**
 * A move is reported, never committed: this story writes it itself — which is
 * the consumer's job, and the only reason the cards appear to move at all.
 */
function RequestsBoard() {
	const [requests, setRequests] = useState(REQUESTS);
	const [said, setSaid] = useState<string | null>(null);
	return (
		<div className="max-w-5xl p-4">
			<Board<Request>
				items={requests}
				columns={COLUMNS}
				columnOf={(request) => request.stage}
				idOf={(request) => request.ref}
				labelOf={(request) => request.name}
				searchText={(request) => `${request.name} ${request.owner}`}
				searchLabel="Search by name or owner"
				onOpen={(request) => setSaid(`Opened ${request.name}`)}
				onMove={(request, column) => {
					setSaid(`Moved ${request.name} to ${column.label}`);
					setRequests((all) =>
						all.map((one) =>
							one.ref === request.ref ? { ...one, stage: column.id } : one,
						),
					);
				}}
				renderCard={(request) => (
					<>
						<div className="type-card-title-sm">{request.name}</div>
						<div
							className="mt-2 text-muted-foreground type-card-metadata"
							style={{
								display: "flex",
								flexWrap: "wrap",
								alignItems: "center",
								gap: "0.375rem",
							}}
						>
							<Badge variant="outline">{request.owner}</Badge>
							<span>{request.opened}</span>
						</div>
					</>
				)}
			/>
			<p className="mt-4 text-muted-foreground type-card-metadata">
				{said ?? "Drag a card onto another column, or use its Move control."}
			</p>
		</div>
	);
}

export const RequestsByStage = { render: () => <RequestsBoard /> };

/** A board nobody may move anything on: cards are read-only and do not drag. */
export const ReadOnly = {
	render: () => (
		<div className="max-w-5xl p-4">
			<Board<Request>
				items={REQUESTS}
				columns={COLUMNS}
				columnOf={(request) => request.stage}
				idOf={(request) => request.ref}
				labelOf={(request) => request.name}
				renderCard={(request) => (
					<div className="type-card-title-sm">{request.name}</div>
				)}
			/>
		</div>
	),
};
