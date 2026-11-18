"use client";

import { useQuery } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { activityLabel } from "@/features/permissions/model/activity-label";
import { permissionLabel } from "@/features/permissions/model/effective";
import { permissionQueries } from "@/features/permissions/service/queries";
import { failureText } from "@/shared/lib/failure-text";
import {
	Button,
	Checkbox,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
	ErrorState,
	Field,
	Input,
	Label,
	Spinner,
	useLoadingPhase,
} from "@/shared/ui";
import { useCreateRole } from "../service/mutations";

export function RoleForm({ orgId }: { orgId: string }) {
	const [error, setError] = useState<string | null>(null);
	const [open, setOpen] = useState(false);
	const [nameError, setNameError] = useState<string>();
	const [name, setName] = useState("");
	const [description, setDescription] = useState("");
	const [permissions, setPermissions] = useState<
		{ resource: string; action: string }[]
	>([]);
	const createRole = useCreateRole();
	const catalogue = useQuery({
		...permissionQueries.serviceInfo(),
		enabled: open,
	});
	const { quiet, indicator } = useLoadingPhase(open && catalogue.isPending);
	const activities = catalogue.data?.capabilities?.permissions ?? [];
	function togglePermission(
		permission: { resource: string; action: string },
		checked: boolean,
	) {
		setPermissions((current) =>
			checked
				? [
						...current,
						{ resource: permission.resource, action: permission.action },
					]
				: current.filter(
						(entry) => permissionLabel(entry) !== permissionLabel(permission),
					),
		);
	}

	function reset() {
		setError(null);
		setNameError(undefined);
		setName("");
		setDescription("");
		setPermissions([]);
	}

	function handleSubmit() {
		if (createRole.isPending || catalogue.isPending || catalogue.isError)
			return;
		if (!name.trim()) {
			setNameError("Enter a role name.");
			return;
		}
		setNameError(undefined);
		setError(null);
		createRole.mutate(
			{
				name: name.trim(),
				description: description.trim(),
				permissions,
				orgId,
			},
			{
				onSuccess: () => {
					toast.success(`Role "${name.trim()}" created`);
					reset();
					setOpen(false);
				},
				onError: (cause) => setError(failureText("create this role", cause)),
			},
		);
	}

	return (
		<Dialog
			open={open}
			onOpenChange={(value) => {
				if (createRole.isPending) return;
				if (!value) reset();
				setOpen(value);
			}}
		>
			<DialogTrigger render={<Button />}>
				<Plus className="mr-2 h-4 w-4" />
				Create role
			</DialogTrigger>
			<DialogContent className="sm:max-w-lg">
				<DialogHeader>
					<DialogTitle>Create role</DialogTitle>
					<DialogDescription>
						{orgId
							? "Define a role for the selected organization."
							: "Define a role that can be assigned across organizations."}
					</DialogDescription>
				</DialogHeader>
				<form
					noValidate
					onSubmit={(event) => {
						event.preventDefault();
						handleSubmit();
					}}
				>
					{error && <ErrorState title="Role wasn’t created" detail={error} />}
					<fieldset disabled={createRole.isPending} className="space-y-4 py-4">
						<Field label="Name" error={nameError}>
							{(control) => (
								<Input
									{...control}
									placeholder="e.g. Billing manager"
									value={name}
									onChange={(e) => {
										setName(e.target.value);
										setNameError(undefined);
									}}
								/>
							)}
						</Field>
						<Field
							label="Description"
							description="Optional. Describe who this role is for."
						>
							{(control) => (
								<Input
									{...control}
									value={description}
									onChange={(e) => setDescription(e.target.value)}
								/>
							)}
						</Field>

						<div className="space-y-3">
							<p className="type-label">What people with this role can do</p>
							{catalogue.isError ? (
								<ErrorState
									title="Activities unavailable"
									detail={failureText(
										"load the available activities",
										catalogue.error,
									)}
								/>
							) : indicator ? (
								<Spinner label="Loading available activities" />
							) : quiet ? null : activities.length === 0 ? (
								<p className="type-body text-muted-foreground">
									No activities are available to add. You can create the role
									and add access later.
								</p>
							) : (
								<div className="max-h-64 space-y-3 overflow-y-auto rounded-lg border p-3">
									{activities.map((activity) => {
										const key = permissionLabel(activity);
										return (
											<div key={key} className="flex items-start gap-2">
												<Checkbox
													id={`new-role-${key}`}
													checked={permissions.some(
														(entry) => permissionLabel(entry) === key,
													)}
													onCheckedChange={(checked) =>
														togglePermission(activity, !!checked)
													}
													disabled={
														createRole.isPending || !activityLabel(activity)
													}
												/>
												<Label
													htmlFor={`new-role-${key}`}
													className="leading-relaxed"
												>
													{activityLabel(activity) ||
														"This activity has no description. Ask your administrator to update it."}
												</Label>
											</div>
										);
									})}
								</div>
							)}
						</div>
					</fieldset>

					<DialogFooter>
						<Button
							type="button"
							variant="outline"
							disabled={createRole.isPending}
							onClick={() => {
								reset();
								setOpen(false);
							}}
						>
							Cancel
						</Button>
						<Button
							type="submit"
							disabled={
								createRole.isPending || catalogue.isPending || catalogue.isError
							}
						>
							{createRole.isPending ? "Creating..." : "Create"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
