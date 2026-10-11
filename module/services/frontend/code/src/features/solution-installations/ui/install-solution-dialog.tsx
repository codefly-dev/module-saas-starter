"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useForm, useWatch } from "react-hook-form";
import { toast } from "sonner";
import { useRoles } from "@/features/roles/service/queries";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import { readOutcome, readOutcomeMessage } from "@/shared/lib/read-outcome";
import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	Input,
	Label,
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
import { useInstallSolution } from "../service/mutations";

/**
 * Installs one available target into the organization: the agent the solution
 * acts as, the least-privilege role that agent holds at the installation's
 * authority root, and the ceiling it may ever be minted for. The owner of record
 * is the installing administrator.
 */
export function InstallSolutionDialog({
	orgId,
	solution,
	onClose,
}: {
	orgId: string;
	solution: AvailableSolution;
	onClose: () => void;
}) {
	const install = useInstallSolution(orgId);
	const roles = useRoles(orgId);
	const rolesOutcome = readOutcome(roles.isError, roles.error);
	const roleList = roles.data ?? [];
	const roleLabel = new Map(
		roleList.map((role) => [role.id, roleOptionLabel(role)] as const),
	);

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
	const roleId = useWatch({ control: form.control, name: "roleId" });
	const errors = form.formState.errors;

	const submit = form.handleSubmit((values) =>
		install.mutate(
			{ targetId: solution.targetId, values },
			{
				onSuccess: () => {
					toast.success(`${solution.routeAlias} installed`);
					onClose();
				},
			},
		),
	);

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
					<div className="space-y-2">
						<Label htmlFor="install-agent-identifier">Agent identifier</Label>
						<Input
							id="install-agent-identifier"
							aria-describedby="install-agent-identifier-help"
							className="font-mono"
							{...form.register("agentIdentifier")}
						/>
						<p
							id="install-agent-identifier-help"
							className="text-xs text-muted-foreground"
						>
							The agent the solution acts as, as publisher/name:version.
						</p>
						{errors.agentIdentifier && (
							<p className="text-sm text-destructive">
								{errors.agentIdentifier.message}
							</p>
						)}
					</div>

					<div className="grid gap-4 sm:grid-cols-2">
						<div className="space-y-2">
							<Label htmlFor="install-display-name">
								Agent name (optional)
							</Label>
							<Input
								id="install-display-name"
								placeholder={agentIdentifierFor(solution)}
								{...form.register("displayName")}
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="install-root-label">
								Access boundary name (optional)
							</Label>
							<Input
								id="install-root-label"
								placeholder={solution.routeAlias}
								{...form.register("rootScopeLabel")}
							/>
						</div>
					</div>

					<div className="space-y-2">
						<Label htmlFor="install-role">Agent role</Label>
						<Select
							value={roleId}
							onValueChange={(value) =>
								form.setValue("roleId", value ?? "", { shouldValidate: true })
							}
						>
							<SelectTrigger
								id="install-role"
								aria-label="Agent role"
								aria-describedby="install-role-help"
								className="w-full"
							>
								<SelectValue placeholder="Choose a role…">
									{roleId
										? roleLabel.get(roleId) || "Role unavailable"
										: undefined}
								</SelectValue>
							</SelectTrigger>
							<SelectContent>
								{roleList.length === 0 ? (
									<div className="px-2 py-2 text-sm text-muted-foreground">
										{roles.isLoading
											? "Loading roles…"
											: readOutcomeMessage(
													rolesOutcome,
													"this organization's roles",
													"No roles available.",
												)}
									</div>
								) : (
									roleList.map((role) => (
										<SelectItem key={role.id} value={role.id}>
											{roleOptionLabel(role)}
										</SelectItem>
									))
								)}
							</SelectContent>
						</Select>
						<p id="install-role-help" className="text-xs text-muted-foreground">
							Granted to the solution&apos;s agent at its access boundary.
							Choose the least it needs; widening it later is a separate,
							audited action.
						</p>
						{errors.roleId && (
							<p className="text-sm text-destructive">
								{errors.roleId.message}
							</p>
						)}
					</div>

					<div className="grid gap-4 sm:grid-cols-2">
						<div className="space-y-2">
							<Label htmlFor="install-audiences">Allowed audiences</Label>
							<Textarea
								id="install-audiences"
								aria-describedby="install-audiences-help"
								rows={3}
								className="font-mono text-sm"
								{...form.register("allowedAudiences")}
							/>
							<p
								id="install-audiences-help"
								className="text-xs text-muted-foreground"
							>
								One per line. Empty means unrestricted. The host refuses an
								audience it does not serve and says which it does.
							</p>
						</div>
						<div className="space-y-2">
							<Label htmlFor="install-scopes">Allowed resource kinds</Label>
							<Textarea
								id="install-scopes"
								aria-describedby="install-scopes-help"
								rows={3}
								className="font-mono text-sm"
								{...form.register("allowedScopes")}
							/>
							<p
								id="install-scopes-help"
								className="text-xs text-muted-foreground"
							>
								One per line. Empty means unrestricted.
							</p>
						</div>
					</div>

					{install.isError && (
						<p role="alert" className="text-sm text-destructive">
							The host refused the install: {hostRefusal(install.error)}
						</p>
					)}

					<DialogFooter>
						<Button type="button" variant="outline" onClick={onClose}>
							Cancel
						</Button>
						<Button type="submit" disabled={install.isPending}>
							{install.isPending ? "Installing…" : "Install"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
