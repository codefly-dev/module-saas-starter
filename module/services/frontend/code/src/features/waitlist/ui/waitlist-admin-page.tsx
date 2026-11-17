"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { createClient } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
	Spinner,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
	useLoadingPhase,
} from "@/shared/ui";

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

export function WaitlistAdminPage() {
	const queryClient = useQueryClient();
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
	const { indicator, quiet } = useLoadingPhase(entries.isLoading);
	const outcome = readOutcome(entries.isError, entries.error);

	return (
		<PageBody>
			<PageHeader
				title="Waitlist"
				description="Review requests for access and where each one came from."
			/>
			<Card>
				<CardHeader>
					<CardTitle>Access requests</CardTitle>
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
					<Panel className="overflow-x-auto p-0">
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Person</TableHead>
									<TableHead>Status</TableHead>
									<TableHead>Came from</TableHead>
									<TableHead>Created</TableHead>
									<TableHead className="text-right">Actions</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{indicator && (
									<TableRow>
										<TableCell colSpan={5}>
											<Spinner label="Loading access requests" size="sm" />
										</TableCell>
									</TableRow>
								)}
								{entries.isError && (
									<TableRow>
										<TableCell colSpan={5}>
											<ErrorState
												title="Couldn't load the access requests"
												detail={
													outcome === "forbidden"
														? "Your account doesn't have the platform role this list needs."
														: "Reload the page to try again. If it keeps failing, tell your administrator."
												}
											/>
										</TableCell>
									</TableRow>
								)}
								{!quiet &&
									!indicator &&
									!entries.isError &&
									entries.data?.entries.length === 0 && (
									<TableRow>
										<TableCell colSpan={5}>
											No matching access requests.
										</TableCell>
									</TableRow>
								)}
								{entries.data?.entries.map((entry) => (
									<TableRow key={entry.id}>
										<TableCell>
											<div className="font-medium">{entry.email}</div>
											<div className="text-xs text-muted-foreground">
												{[entry.name, entry.company]
													.filter(Boolean)
													.join(" · ")}
											</div>
										</TableCell>
										<TableCell>
											<Badge variant="outline">{stateLabel(entry.state)}</Badge>
										</TableCell>
										<TableCell>
											{[entry.source, entry.campaign]
												.filter(Boolean)
												.join(" / ") || "Direct"}
										</TableCell>
										<TableCell>
											{entry.createdAt
												? timestampDate(entry.createdAt).toLocaleDateString()
												: "—"}
										</TableCell>
										<TableCell>
											<div className="flex justify-end gap-1">
												{(entry.state === WaitlistState.PENDING ||
													entry.state === WaitlistState.VERIFIED) && (
													<>
														<Button
															size="sm"
															variant="ghost"
															aria-label={`Approve ${entry.email}`}
															onClick={() =>
																review.mutate({
																	entry,
																	nextState: WaitlistState.APPROVED,
																})
															}
														>
															<Check className="h-4 w-4" />
														</Button>
														<Button
															size="sm"
															variant="ghost"
															aria-label={`Reject ${entry.email}`}
															onClick={() =>
																review.mutate({
																	entry,
																	nextState: WaitlistState.REJECTED,
																})
															}
														>
															<X className="h-4 w-4" />
														</Button>
													</>
												)}
												{entry.state === WaitlistState.APPROVED && (
													<Button
														size="sm"
														variant="ghost"
														aria-label={`Invite ${entry.email}`}
														onClick={() => invite.mutate(entry.id)}
													>
														<Mail className="h-4 w-4" />
													</Button>
												)}
											</div>
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					</Panel>
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
		</PageBody>
	);
}
