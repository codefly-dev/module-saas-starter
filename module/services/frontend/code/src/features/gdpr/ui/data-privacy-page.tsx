"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Download, FileArchive, Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import {
	AlertDialog,
	AlertDialogAction,
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

	const exportMutation = useMutation({
		mutationFn: () => gdprMutations.requestExport(),
		onSuccess: () => {
			toast.success(
				"Data export requested. You will be notified when it is ready.",
			);
			queryClient.invalidateQueries({ queryKey: ["gdpr"] });
		},
		onError: (error) => toast.error(failureText("request the export", error)),
	});

	const deletionMutation = useMutation({
		mutationFn: () => gdprMutations.requestDeletion(),
		onSuccess: () => {
			toast.success("Account deletion requested.");
			queryClient.invalidateQueries({ queryKey: ["gdpr"] });
		},
		onError: (error) =>
			toast.error(failureText("request account deletion", error)),
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
								<CardTitle>Export my data</CardTitle>
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
					</CardContent>
					<CardFooter>
						<Button
							onClick={() => exportMutation.mutate()}
							disabled={!exportAvailable || exportMutation.isPending}
						>
							{exportMutation.isPending ? (
								<>
									<Loader2 className="animate-spin" />
									Requesting…
								</>
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
								<CardTitle>Delete my account</CardTitle>
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
						<AlertDialog>
							<AlertDialogTrigger
								render={
									<Button variant="destructive" disabled={!deletionAvailable}>
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
								<AlertDialogFooter>
									<AlertDialogCancel>Cancel</AlertDialogCancel>
									<AlertDialogAction
										onClick={() => deletionMutation.mutate()}
										className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
									>
										{deletionMutation.isPending
											? "Requesting…"
											: "Yes, delete my account"}
									</AlertDialogAction>
								</AlertDialogFooter>
							</AlertDialogContent>
						</AlertDialog>
					</CardFooter>
				</Card>
			</Grid>
		</PageBody>
	);
}
