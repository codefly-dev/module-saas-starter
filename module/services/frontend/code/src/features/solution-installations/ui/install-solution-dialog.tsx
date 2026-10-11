"use client";

import { Field } from "@codefly-dev/ui/layout";
import { zodResolver } from "@hookform/resolvers/zod";
import { Controller, useForm } from "react-hook-form";
import { useRoles } from "@/features/roles/service/queries";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import {
	mayKeepRetainedRows,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	Input,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Textarea,
} from "@/shared/ui";
import {
	agentIdentifierFor,
	generationText,
	hostRefusal,
	releaseText,
	roleOptionLabel,
} from "../model/installations";
import {
	type InstallSolutionValues,
	installSolutionSchema,
} from "../model/schemas";

/** The page owns the operation so closing this form cannot discard a refusal. */
export function InstallSolutionDialog({
	orgId,
	solution,
	onSubmit,
	isPending,
	error,
	onClose,
}: {
	orgId: string;
	solution: AvailableSolution;
	onSubmit: (values: InstallSolutionValues) => void;
	isPending: boolean;
	error?: string;
	onClose: () => void;
}) {
	const roles = useRoles(orgId);
	const outcome = readOutcome(roles.isError, roles.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const roleList = withheld ? [] : (roles.data ?? []);
	const roleLabel = new Map(
		roleList.map((role) => [role.id, roleOptionLabel(role)] as const),
	);
	const readError = roles.isError
		? `${roleList.length ? staleReadNotice(outcome, "this organization's roles") : readOutcomeMessage(outcome, "this organization's roles", "No roles available.")} ${hostRefusal(roles.error)}`
		: undefined;
	const form = useForm<InstallSolutionValues>({
		resolver: zodResolver(installSolutionSchema),
		defaultValues: {
			agentIdentifier: agentIdentifierFor(solution),
			displayName: "",
			rootScopeLabel: "",
			roleId: "",
			allowedAudiences: "",
			allowedScopes: "",
		},
	});
	const errors = form.formState.errors;
	const submit = form.handleSubmit((values) => {
		if (isPending || withheld || roles.isPending) return;
		if (!roleLabel.has(values.roleId)) {
			form.setError(
				"roleId",
				{ message: "Choose an available role" },
				{ shouldFocus: true },
			);
			return;
		}
		onSubmit(values);
	});

	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="sm:max-w-[560px]">
				<DialogHeader>
					<DialogTitle>Install {solution.routeAlias}</DialogTitle>
					<DialogDescription>
						{releaseText(solution)} · {generationText(solution)}. You become its
						owner of record.
					</DialogDescription>
				</DialogHeader>
				<form onSubmit={submit} className="space-y-4" noValidate>
					<Field
						label="Agent identifier"
						description="The agent the solution acts as, as publisher/name:version."
						error={errors.agentIdentifier?.message}
					>
						{(control) => (
							<Input
								{...control}
								className="font-mono"
								{...form.register("agentIdentifier")}
							/>
						)}
					</Field>
					<div className="grid gap-4 sm:grid-cols-2">
						<Field
							label="Agent name (optional)"
							error={errors.displayName?.message}
						>
							{(control) => (
								<Input
									{...control}
									placeholder={agentIdentifierFor(solution)}
									{...form.register("displayName")}
								/>
							)}
						</Field>
						<Field
							label="Access boundary name (optional)"
							error={errors.rootScopeLabel?.message}
						>
							{(control) => (
								<Input
									{...control}
									placeholder={solution.routeAlias}
									{...form.register("rootScopeLabel")}
								/>
							)}
						</Field>
					</div>
					<Controller
						control={form.control}
						name="roleId"
						render={({ field }) => (
							<Field
								label="Agent role"
								error={
									readError && !roleList.length
										? readError
										: errors.roleId?.message
								}
								description={
									<>
										Granted to the solution&apos;s agent at its access boundary.
										Choose the least it needs; widening it later is a separate,
										audited action.
										{readError && roleList.length > 0 && (
											<span role="status" className="block">
												{readError}
											</span>
										)}
										{!roles.isPending && !roles.isError && !roleList.length && (
											<span className="block">No roles available.</span>
										)}
									</>
								}
							>
								{(control) => (
									<Select
										value={withheld ? "" : field.value}
										onValueChange={(value) => field.onChange(value ?? "")}
										disabled={withheld || roles.isPending || !roleList.length}
									>
										<SelectTrigger
											{...control}
											ref={field.ref}
											onBlur={field.onBlur}
											className="w-full"
										>
											<SelectValue placeholder="Choose a role…">
												{!withheld && field.value
													? roleLabel.get(field.value) || "Role unavailable"
													: undefined}
											</SelectValue>
										</SelectTrigger>
										<SelectContent>
											{roleList.map((role) => (
												<SelectItem key={role.id} value={role.id}>
													{roleOptionLabel(role)}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
								)}
							</Field>
						)}
					/>
					<div className="grid gap-4 sm:grid-cols-2">
						<Field
							label="Allowed audiences"
							description="One per line. Empty means unrestricted. The host refuses an audience it does not serve and says which it does."
							error={errors.allowedAudiences?.message}
						>
							{(control) => (
								<Textarea
									{...control}
									rows={3}
									className="font-mono text-sm"
									{...form.register("allowedAudiences")}
								/>
							)}
						</Field>
						<Field
							label="Allowed resource kinds"
							description="One per line. Empty means unrestricted."
							error={errors.allowedScopes?.message}
						>
							{(control) => (
								<Textarea
									{...control}
									rows={3}
									className="font-mono text-sm"
									{...form.register("allowedScopes")}
								/>
							)}
						</Field>
					</div>
					{error && (
						<p role="alert" className="text-sm text-destructive">
							The host refused the install: {error}
						</p>
					)}
					<DialogFooter>
						<Button type="button" variant="outline" onClick={onClose}>
							Cancel
						</Button>
						<Button
							type="submit"
							disabled={isPending || withheld || roles.isPending}
						>
							{isPending ? "Installing…" : "Install"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
