"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Download, FileArchive, Trash2 } from "lucide-react";
import { useRef, useState } from "react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import {
	AlertDialog,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
	DelayedLoading,
	ErrorState,
	Grid,
	Page as PageBody,
	PageHeader,
} from "@/shared/ui";
import { gdprMutations } from "../service/mutations";

export interface DataPrivacyPageProps {
	exportAvailable: boolean;
	deletionAvailable: boolean;
}

export function DataPrivacyPage({
	exportAvailable,
	deletionAvailable,
}: DataPrivacyPageProps) {
	const queryClient = useQueryClient();
	const [confirming, setConfirming] = useState(false);
	const deleting = useRef(false);
	const exporting = useRef(false);

	const exportMutation = useMutation({
		mutationFn: () => gdprMutations.requestExport(),
		onSuccess: () => {
			toast.success(
				"Data export requested. You will be notified when it is ready.",
			);
			queryClient.invalidateQueries({ queryKey: ["gdpr"] });
		},
		onSettled: () => {
			exporting.current = false;
		},
	});

	const deletionMutation = useMutation({
		mutationFn: () => gdprMutations.requestDeletion(),
		onSuccess: () => {
			setConfirming(false);
			toast.success("Account deletion requested.");
			queryClient.invalidateQueries({ queryKey: ["gdpr"] });
		},
		onSettled: () => {
			deleting.current = false;
		},
	});

	return (
		<PageBody>
			<PageHeader
				title="Data and privacy"
				description="Get a copy of your data, or ask for your account to be deleted."
			/>

			<Grid cols={2}>
				<Card>
					<CardHeader>
						<div className="flex items-center gap-3">
							<FileArchive className="size-5 text-muted-foreground" />
							<div>
								<CardTitle role="heading" aria-level={2}>
									Export my data
								</CardTitle>
								<CardDescription>
									A copy of the data held about you.
								</CardDescription>
							</div>
						</div>
					</CardHeader>
					<CardContent>
						<p className="text-sm text-muted-foreground">
							{exportAvailable
								? "Ask for a copy of your data. You will be notified when it is ready."
								: "Data export is not available here yet. To get a copy of your data, contact your administrator."}
						</p>
						{exportMutation.isError && (
							<ErrorState
								title="Export wasn’t requested"
								detail={failureText("request the export", exportMutation.error)}
							/>
						)}
					</CardContent>
					<CardFooter>
						<Button
							onClick={() => {
								if (!exportAvailable || exporting.current) return;
								exporting.current = true;
								exportMutation.mutate();
							}}
							disabled={!exportAvailable || exportMutation.isPending}
						>
							<DelayedLoading
								active={exportMutation.isPending}
								label="Requesting data export"
							/>
							{exportMutation.isPending ? (
								<>Requesting…</>
							) : (
								<>
									<Download />
									Request export
								</>
							)}
						</Button>
					</CardFooter>
				</Card>

				<Card>
					<CardHeader>
						<div className="flex items-center gap-3">
							<Trash2 className="size-5 text-destructive" />
							<div>
								<CardTitle role="heading" aria-level={2}>
									Delete my account
								</CardTitle>
								<CardDescription>
									Close your account and remove your data.
								</CardDescription>
							</div>
						</div>
					</CardHeader>
					<CardContent>
						<p className="text-sm text-muted-foreground">
							{deletionAvailable
								? "Ask for your account to be deleted. Some records may be kept for as long as the retention policy requires."
								: "Account deletion is not available here yet. To have your account deleted, contact your administrator."}
						</p>
					</CardContent>
					<CardFooter>
						<AlertDialog
							open={confirming}
							onOpenChange={(open) => {
								if (deleting.current) return;
								if (open) deletionMutation.reset();
								setConfirming(open);
							}}
						>
							<AlertDialogTrigger
								render={
									<Button variant="outline" disabled={!deletionAvailable}>
										<Trash2 />
										Delete account
									</Button>
								}
							/>
							<AlertDialogContent>
								<AlertDialogHeader>
									<AlertDialogTitle>Delete your account?</AlertDialogTitle>
									<AlertDialogDescription>
										This asks for your account to be deleted. Some records may
										be kept for as long as the retention policy requires.
									</AlertDialogDescription>
								</AlertDialogHeader>
								{deletionMutation.isError && (
									<ErrorState
										title="Account deletion wasn’t requested"
										detail={failureText(
											"request account deletion",
											deletionMutation.error,
										)}
									/>
								)}
								<AlertDialogFooter>
									<AlertDialogCancel disabled={deletionMutation.isPending}>
										Cancel
									</AlertDialogCancel>
									<Button
										onClick={() => {
											if (!deletionAvailable || deleting.current) return;
											deleting.current = true;
											deletionMutation.mutate();
										}}
										disabled={deletionMutation.isPending}
										variant="destructive"
									>
										{deletionMutation.isPending
											? "Requesting…"
											: "Yes, delete my account"}
									</Button>
								</AlertDialogFooter>
							</AlertDialogContent>
						</AlertDialog>
					</CardFooter>
				</Card>
			</Grid>
		</PageBody>
	);
}
