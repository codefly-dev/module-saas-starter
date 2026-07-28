"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Download, FileArchive, Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
	capabilityManifest,
	publicCapabilities,
	starterDefaultCapabilityContext,
} from "@/features/trust/model/capabilities";
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
} from "@/shared/ui";
import { gdprMutations } from "../service/mutations";

export function DataPrivacyPage() {
	const queryClient = useQueryClient();
	const capabilities = publicCapabilities(
		capabilityManifest,
		starterDefaultCapabilityContext,
	);
	const exportAvailable =
		capabilities.find(
			(capability) => capability.id === "privacy.export-artifact",
		)?.state === "operationally_verified";
	const deletionAvailable =
		capabilities.find(
			(capability) => capability.id === "privacy.deletion-completion",
		)?.state === "operationally_verified";

	const exportMutation = useMutation({
		mutationFn: () => gdprMutations.requestExport(),
		onSuccess: () => {
			toast.success("Data export requested. You will be notified when ready.");
			queryClient.invalidateQueries({ queryKey: ["gdpr"] });
		},
		onError: () => toast.error("Failed to request data export"),
	});

	const deletionMutation = useMutation({
		mutationFn: () => gdprMutations.requestDeletion(),
		onSuccess: () => {
			toast.success("Account deletion requested.");
			queryClient.invalidateQueries({ queryKey: ["gdpr"] });
		},
		onError: () => toast.error("Failed to request account deletion"),
	});

	return (
		<div className="space-y-6">
			<div>
				<h2 className="text-2xl font-bold tracking-tight">Data Privacy</h2>
				<p className="text-muted-foreground">
					Review the starter&apos;s data-request workflows and the production
					adapters your deployment still requires.
				</p>
			</div>

			<div className="grid gap-4 md:grid-cols-2">
				<Card>
					<CardHeader>
						<div className="flex items-center gap-3">
							<FileArchive className="h-5 w-5 text-primary" />
							<div>
								<CardTitle className="text-lg">Export My Data</CardTitle>
								<CardDescription>
									Requires a verified secure export adapter.
								</CardDescription>
							</div>
						</div>
					</CardHeader>
					<CardContent>
						<p className="text-sm text-muted-foreground">
							The starter includes a request and status workflow, but its
							placeholder artifact is not a production download. Configure
							subject-bound encrypted storage, expiry, deletion, and dataset
							completeness before enabling this action.
						</p>
					</CardContent>
					<CardFooter>
						<Button
							onClick={() => exportMutation.mutate()}
							disabled={!exportAvailable || exportMutation.isPending}
						>
							{exportMutation.isPending ? (
								<>
									<Loader2 className="mr-2 h-4 w-4 animate-spin" />
									Requesting...
								</>
							) : (
								<>
									<Download className="mr-2 h-4 w-4" />
									{exportAvailable
										? "Request Export"
										: "Export Adapter Required"}
								</>
							)}
						</Button>
					</CardFooter>
				</Card>

				<Card className="border-destructive/30">
					<CardHeader>
						<div className="flex items-center gap-3">
							<Trash2 className="h-5 w-5 text-destructive" />
							<div>
								<CardTitle className="text-lg">Delete My Account</CardTitle>
								<CardDescription>
									Requires verified deletion and retention policy.
								</CardDescription>
							</div>
						</div>
					</CardHeader>
					<CardContent>
						<p className="text-sm text-muted-foreground">
							The starter&apos;s current anonymization path does not cover every
							product dataset, provider, legal hold, financial record, or
							backup. Keep this action unavailable until those rules and
							adapters are complete.
						</p>
					</CardContent>
					<CardFooter>
						<AlertDialog>
							<AlertDialogTrigger
								render={
									<Button variant="destructive" disabled={!deletionAvailable}>
										<Trash2 className="mr-2 h-4 w-4" />
										{deletionAvailable
											? "Delete Account"
											: "Deletion Policy Required"}
									</Button>
								}
							/>
							<AlertDialogContent>
								<AlertDialogHeader>
									<AlertDialogTitle>Are you absolutely sure?</AlertDialogTitle>
									<AlertDialogDescription>
										This action follows the configured deletion and retention
										policy. Required retained records and backup-expiry behavior
										remain governed by that reviewed policy.
									</AlertDialogDescription>
								</AlertDialogHeader>
								<AlertDialogFooter>
									<AlertDialogCancel>Cancel</AlertDialogCancel>
									<AlertDialogAction
										onClick={() => deletionMutation.mutate()}
										className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
									>
										{deletionMutation.isPending
											? "Requesting..."
											: "Yes, delete my account"}
									</AlertDialogAction>
								</AlertDialogFooter>
							</AlertDialogContent>
						</AlertDialog>
					</CardFooter>
				</Card>
			</div>

			<Card>
				<CardHeader>
					<CardTitle className="text-lg">Production enablement</CardTitle>
					<CardDescription>
						An adopter must connect durable jobs and provider cleanup, define
						dataset ownership and retention exceptions, and record current
						environment-scoped evidence before these controls become available.
					</CardDescription>
				</CardHeader>
			</Card>
		</div>
	);
}
