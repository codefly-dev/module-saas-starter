"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { createClient } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Check, Mail, Search, X } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import {
	type WaitlistEntry,
	WaitlistService,
	WaitlistState,
} from "@/gen/saas/accounts/v1/waitlist_pb";
import { apiTransport } from "@/lib/connect/transport";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Badge,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	ErrorState,
	Input,
	Page as PageBody,
	PageHeader,
	Panel,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";

const client = createClient(WaitlistService, apiTransport);

// What each state is called on screen, in the reviewer's words.
const STATE_LABELS: Partial<Record<WaitlistState, string>> = {
	[WaitlistState.PENDING]: "Waiting for review",
	[WaitlistState.VERIFIED]: "Email confirmed",
	[WaitlistState.APPROVED]: "Approved",
	[WaitlistState.REJECTED]: "Rejected",
	[WaitlistState.INVITED]: "Invited",
	[WaitlistState.CONVERTED]: "Signed up",
	[WaitlistState.UNSUBSCRIBED]: "Unsubscribed",
};

function stateLabel(state: WaitlistState): string {
	return STATE_LABELS[state] ?? "Unknown";
}

const NO_ENTRIES: WaitlistEntry[] = [];
const column = createColumnHelper<WaitlistEntry>();

export function WaitlistAdminPage() {
	const queryClient = useQueryClient();
	const [rejecting, setRejecting] = useState<WaitlistEntry | null>(null);
	const [query, setQuery] = useState("");
	const [state, setState] = useState(WaitlistState.UNSPECIFIED);
	const [source, setSource] = useState("");
	const [campaign, setCampaign] = useState("");
	const filterKey = JSON.stringify([state, query, source, campaign]);
	const [pagination, setPagination] = useState({
		filterKey,
		pageToken: "",
		history: [] as string[],
	});
	const pageToken =
		pagination.filterKey === filterKey ? pagination.pageToken : "";
	const pageHistory =
		pagination.filterKey === filterKey ? pagination.history : [];
	const entries = useQuery({
		queryKey: ["waitlist", state, query, source, campaign, pageToken],
		queryFn: () =>
			client.list({
				state,
				query,
				source,
				campaign,
				pageSize: 100,
				pageToken,
			}),
	});
	const review = useMutation({
		mutationFn: ({
			entry,
			nextState,
		}: {
			entry: WaitlistEntry;
			nextState: WaitlistState;
		}) =>
			client.review({
				id: entry.id,
				state: nextState,
				adminNotes: entry.adminNotes,
				tags: entry.tags,
			}),
		onSuccess: () => {
			setRejecting(null);
			queryClient.invalidateQueries({ queryKey: ["waitlist"] });
			toast.success("Access request updated");
		},
		onError: (error) =>
			toast.error(failureText("update this access request", error)),
	});
	const invite = useMutation({
		mutationFn: (id: string) => client.invite({ id }),
		onSuccess: () => {
			queryClient.invalidateQueries({ queryKey: ["waitlist"] });
			toast.success("Access invitation queued");
		},
		onError: (error) => toast.error(failureText("send the invitation", error)),
	});
	const outcome = readOutcome(entries.isError, entries.error);

	const columns = [
		column.accessor("email", {
			header: "Person",
			cell: ({ row }) => (
				<div>
					<p className="font-medium">{row.original.email}</p>
					<p className="text-sm text-muted-foreground">
						{[row.original.name, row.original.company]
							.filter(Boolean)
							.join(" · ")}
					</p>
				</div>
			),
		}),
		column.accessor("state", {
			header: "Status",
			cell: ({ getValue }) => (
				<Badge variant="outline">{stateLabel(getValue())}</Badge>
			),
		}),
		column.display({
			id: "source",
			header: "Came from",
			cell: ({ row }) =>
				[row.original.source, row.original.campaign]
					.filter(Boolean)
					.join(" / ") || "Direct",
		}),
		column.accessor("createdAt", {
			header: "Created",
			cell: ({ getValue }) => {
				const date = getValue();
				return date ? timestampDate(date).toLocaleDateString() : "—";
			},
		}),
		column.display({
			id: "actions",
			header: "Actions",
			cell: ({ row }) => {
				const entry = row.original;
				return (
					<div className="flex flex-wrap gap-2">
						{(entry.state === WaitlistState.PENDING ||
							entry.state === WaitlistState.VERIFIED) && (
							<>
								<Button
									size="sm"
									variant="outline"
									disabled={review.isPending}
									aria-label={`Approve ${entry.email}`}
									onClick={() =>
										review.mutate({ entry, nextState: WaitlistState.APPROVED })
									}
								>
									<Check aria-hidden />
									Approve
								</Button>
								<Button
									size="sm"
									variant="ghost"
									disabled={review.isPending}
									aria-label={`Reject ${entry.email}`}
									onClick={() => setRejecting(entry)}
								>
									<X aria-hidden />
									Reject
								</Button>
							</>
						)}
						{entry.state === WaitlistState.APPROVED && (
							<Button
								size="sm"
								variant="outline"
								disabled={invite.isPending}
								aria-label={`Invite ${entry.email}`}
								onClick={() => invite.mutate(entry.id)}
							>
								<Mail aria-hidden />
								Invite
							</Button>
						)}
					</div>
				);
			},
		}),
	];
	const table = useReactTable({
		data: entries.data?.entries ?? NO_ENTRIES,
		columns,
		getCoreRowModel: getCoreRowModel(),
	});

	return (
		<PageBody>
			<PageHeader
				title="Waitlist"
				description="Review requests for access and where each one came from."
			/>
			<Card>
				<CardHeader>
					<CardTitle role="heading" aria-level={2}>
						Access requests
					</CardTitle>
					<CardDescription>
						Every approval and invitation is recorded in the audit log.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-4">
					<div className="grid gap-3 md:grid-cols-4">
						<div className="relative">
							<Search className="absolute left-3 top-2.5 h-4 w-4 text-muted-foreground" />
							<Input
								aria-label="Search waitlist"
								className="pl-9"
								value={query}
								onChange={(event) => setQuery(event.target.value)}
								placeholder="Email, name, company"
							/>
						</div>
						<Select
							value={String(state)}
							items={Object.values(WaitlistState)
								.filter((value): value is number => typeof value === "number")
								.map((value) => ({
									value: String(value),
									label:
										value === WaitlistState.UNSPECIFIED
											? "All statuses"
											: stateLabel(value),
								}))}
							onValueChange={(value) =>
								setState(Number(value) as WaitlistState)
							}
						>
							<SelectTrigger aria-label="Filter by status">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value={String(WaitlistState.UNSPECIFIED)}>
									All statuses
								</SelectItem>
								{Object.values(WaitlistState)
									.filter(
										(value): value is number =>
											typeof value === "number" &&
											value !== WaitlistState.UNSPECIFIED,
									)
									.map((value) => (
										<SelectItem key={value} value={String(value)}>
											{stateLabel(value)}
										</SelectItem>
									))}
							</SelectContent>
						</Select>
						<Input
							aria-label="Filter by source"
							value={source}
							onChange={(event) => setSource(event.target.value)}
							placeholder="Source"
						/>
						<Input
							aria-label="Filter by campaign"
							value={campaign}
							onChange={(event) => setCampaign(event.target.value)}
							placeholder="Campaign"
						/>
					</div>
					{entries.isError ? (
						<Panel className="space-y-3">
							<ErrorState
								title="Couldn't load the access requests"
								detail={
									outcome === "forbidden"
										? "You don't have access to these requests. Ask a platform administrator for access."
										: "The requests could not be loaded. Try again."
								}
							/>
							{outcome !== "forbidden" && (
								<Button
									variant="outline"
									onClick={() => void entries.refetch()}
								>
									Try again
								</Button>
							)}
						</Panel>
					) : (
						<DataTable
							table={table}
							isLoading={entries.isLoading}
							emptyMessage={
								query ||
								source ||
								campaign ||
								state !== WaitlistState.UNSPECIFIED
									? "No matching access requests. Change the filters to see more."
									: "No access requests yet. New requests will appear here for review."
							}
						/>
					)}
					<div className="flex justify-end gap-2">
						<Button
							variant="outline"
							disabled={pageHistory.length === 0 || entries.isFetching}
							onClick={() => {
								const previous = pageHistory[pageHistory.length - 1] ?? "";
								setPagination({
									filterKey,
									pageToken: previous,
									history: pageHistory.slice(0, -1),
								});
							}}
						>
							Previous
						</Button>
						<Button
							variant="outline"
							disabled={!entries.data?.nextPageToken || entries.isFetching}
							onClick={() => {
								setPagination({
									filterKey,
									pageToken: entries.data?.nextPageToken ?? "",
									history: [...pageHistory, pageToken],
								});
							}}
						>
							Next
						</Button>
					</div>
				</CardContent>
			</Card>
			<AlertDialog
				open={Boolean(rejecting)}
				onOpenChange={(open) => {
					if (!open && !review.isPending) setRejecting(null);
				}}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Reject access request?</AlertDialogTitle>
						<AlertDialogDescription>
							Reject the request from {rejecting?.email}? They will not be
							invited through this request.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel disabled={review.isPending}>
							Cancel
						</AlertDialogCancel>
						<AlertDialogAction
							disabled={review.isPending}
							onClick={() => {
								if (rejecting)
									review.mutate({
										entry: rejecting,
										nextState: WaitlistState.REJECTED,
									});
							}}
						>
							{review.isPending ? "Rejecting…" : "Reject request"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</PageBody>
	);
}
