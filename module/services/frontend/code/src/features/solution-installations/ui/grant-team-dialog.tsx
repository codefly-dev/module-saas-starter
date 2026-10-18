"use client";

import { Field } from "@codefly-dev/ui/layout";
import { zodResolver } from "@hookform/resolvers/zod";
import { useQuery } from "@tanstack/react-query";
import { Controller, useForm } from "react-hook-form";
import { useRoles } from "@/features/roles/service/queries";
import { teamQueries } from "@/features/teams/service/queries";
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
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import { hostRefusal, roleOptionLabel } from "../model/installations";
import { type GrantTeamValues, grantTeamSchema } from "../model/schemas";

/** The host resolves the grant's scope; the page owns its eventual result. */
export function GrantTeamDialog({
	orgId,
	solution,
	onSubmit,
	isPending,
	error,
	onClose,
}: {
	orgId: string;
	solution: string;
	onSubmit: (values: GrantTeamValues) => void;
	isPending: boolean;
	error?: string;
	onClose: () => void;
}) {
	const teams = useQuery(teamQueries.list(orgId));
	const roles = useRoles(orgId);
	const teamsOutcome = readOutcome(teams.isError, teams.error);
	const rolesOutcome = readOutcome(roles.isError, roles.error);
	const teamsWithheld = !mayKeepRetainedRows(teamsOutcome);
	const rolesWithheld = !mayKeepRetainedRows(rolesOutcome);
	const teamList = teamsWithheld ? [] : (teams.data?.teams ?? []);
	const roleList = rolesWithheld ? [] : (roles.data ?? []);
	const teamName = new Map(
		teamList.map((team) => [team.id, team.name] as const),
	);
	const roleLabel = new Map(
		roleList.map((role) => [role.id, roleOptionLabel(role)] as const),
	);
	const teamReadError = teams.isError
		? `${teamList.length ? staleReadNotice(teamsOutcome, "this organization's teams") : readOutcomeMessage(teamsOutcome, "this organization's teams", "This organization has no team yet.")} ${hostRefusal(teams.error)}`
		: undefined;
	const roleReadError = roles.isError
		? `${roleList.length ? staleReadNotice(rolesOutcome, "this organization's roles") : readOutcomeMessage(rolesOutcome, "this organization's roles", "No roles available.")} ${hostRefusal(roles.error)}`
		: undefined;
	const form = useForm<GrantTeamValues>({
		resolver: zodResolver(grantTeamSchema),
		defaultValues: { teamId: "", roleId: "" },
	});
	const errors = form.formState.errors;
	const blocked =
		isPending ||
		teamsWithheld ||
		rolesWithheld ||
		teams.isPending ||
		roles.isPending;
	const submit = form.handleSubmit((values) => {
		if (blocked) return;
		if (!teamName.has(values.teamId)) {
			form.setError(
				"teamId",
				{ message: "Choose an available team" },
				{ shouldFocus: true },
			);
			return;
		}
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
			<DialogContent className="sm:max-w-[480px]">
				<DialogHeader>
					<DialogTitle>Grant access to {solution}</DialogTitle>
					<DialogDescription>
						Choose who gets access and at which level: a team, and the role it
						holds at this installation. Availability and effective access are
						reported by the host.
					</DialogDescription>
				</DialogHeader>
				<form onSubmit={submit} className="space-y-4" noValidate>
					<Controller
						control={form.control}
						name="teamId"
						render={({ field }) => (
							<Field
								label="Team"
								error={
									teamReadError && !teamList.length
										? teamReadError
										: errors.teamId?.message
								}
								description={
									teamReadError && teamList.length ? (
										<span role="status">{teamReadError}</span>
									) : !teams.isPending && !teams.isError && !teamList.length ? (
										"This organization has no team yet."
									) : undefined
								}
							>
								{(control) => (
									<Select
										value={teamsWithheld ? "" : field.value}
										onValueChange={(value) => field.onChange(value ?? "")}
										disabled={
											teamsWithheld || teams.isPending || !teamList.length
										}
									>
										<SelectTrigger
											{...control}
											ref={field.ref}
											onBlur={field.onBlur}
											className="w-full"
										>
											<SelectValue placeholder="Choose a team…">
												{!teamsWithheld && field.value
													? teamName.get(field.value) || "Team unavailable"
													: undefined}
											</SelectValue>
										</SelectTrigger>
										<SelectContent>
											{teamList.map((team) => (
												<SelectItem key={team.id} value={team.id}>
													{team.name}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
								)}
							</Field>
						)}
					/>
					<Controller
						control={form.control}
						name="roleId"
						render={({ field }) => (
							<Field
								label="Access level (role)"
								error={
									roleReadError && !roleList.length
										? roleReadError
										: errors.roleId?.message
								}
								description={
									<>
										What the team may do inside the solution. The role must
										permit using a solution, or the host refuses the grant.
										{roleReadError && roleList.length > 0 && (
											<span role="status" className="block">
												{roleReadError}
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
										value={rolesWithheld ? "" : field.value}
										onValueChange={(value) => field.onChange(value ?? "")}
										disabled={
											rolesWithheld || roles.isPending || !roleList.length
										}
									>
										<SelectTrigger
											{...control}
											ref={field.ref}
											onBlur={field.onBlur}
											className="w-full"
										>
											<SelectValue placeholder="Choose a role…">
												{!rolesWithheld && field.value
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
					{error && (
						<p role="alert" className="text-sm text-destructive">
							The host refused the grant: {error}
						</p>
					)}
					<DialogFooter>
						<Button type="button" variant="outline" onClick={onClose}>
							Cancel
						</Button>
						<Button type="submit" disabled={blocked}>
							{isPending ? "Granting…" : "Grant"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
