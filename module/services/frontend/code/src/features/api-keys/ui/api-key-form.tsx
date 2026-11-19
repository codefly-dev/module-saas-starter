"use client";

import { Radio, RadioGroup } from "@codefly-dev/ui/layout";
import { Check, Copy, Plus } from "lucide-react";
import { useId, useState } from "react";
import { toast } from "sonner";
import { OrgSelector } from "@/components/org-selector";
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
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import { useCreateAPIKey } from "../service/mutations";

// SCOPE_PRESETS — the operator-facing label maps to a list of
// `{resource, action}` scope objects the api persists with the key.
// Wildcards (* in either segment) are honoured by requireScope, so
// "Read all" → `*:read` covers any read-tagged handler.
const SCOPE_PRESETS = [
	{
		id: "read_only",
		label: "Read-only access",
		description:
			"View people, organizations, activity logs, webhooks, and plan details. Cannot make changes.",
		scopes: [{ resource: "*", action: "read" }],
	},
	{
		id: "read_write",
		label: "Read & write",
		description:
			"Read and change application data, except creating API keys. Use only when your integration needs to make changes.",
		scopes: [
			{ resource: "*", action: "read" },
			{ resource: "*", action: "write" },
		],
	},
	{
		id: "webhooks_only",
		label: "Webhook management",
		description:
			"Manage outbound webhooks (create / replay / rotate). Can also list audit events.",
		scopes: [
			{ resource: "webhooks", action: "read" },
			{ resource: "webhooks", action: "write" },
			{ resource: "audit", action: "read" },
		],
	},
	{
		id: "module_envelope",
		label: "Manage application access limits",
		description:
			"View and update the maximum access available to connected applications. Does not allow creating other keys.",
		scopes: [
			{ resource: "module_envelope", action: "read" },
			{ resource: "module_envelope", action: "write" },
		],
	},
	{
		id: "no_scopes",
		label: "No application access",
		description:
			"Use to test that protected actions are denied. This key has no granted access.",
		scopes: [],
	},
] as const;

type ScopePreset = (typeof SCOPE_PRESETS)[number]["id"];

export function APIKeyForm({ orgId }: { orgId: string }) {
	const [open, setOpen] = useState(false);
	const [keyName, setKeyName] = useState("");
	const [environment, setEnvironment] = useState("1");
	const [scopePreset, setScopePreset] = useState<ScopePreset>("read_only");

	// State for showing the plaintext key after creation
	const [plaintextKey, setPlaintextKey] = useState<string | null>(null);
	const [copied, setCopied] = useState(false);
	const [nameError, setNameError] = useState<string>();
	const [error, setError] = useState<string | null>(null);

	const createKey = useCreateAPIKey();
	const formId = useId();

	function reset() {
		setKeyName("");
		setNameError(undefined);
		setEnvironment("1");
		setScopePreset("read_only");
		setPlaintextKey(null);
		setCopied(false);
		setError(null);
	}

	function handleSubmit() {
		if (createKey.isPending) return;
		if (!orgId) {
			setError("Select an organization before creating a key.");
			return;
		}
		if (!keyName.trim()) {
			setNameError("Enter a name for your API key.");
			return;
		}
		setError(null);
		const preset = SCOPE_PRESETS.find((p) => p.id === scopePreset);
		createKey.mutate(
			{
				organizationId: orgId,
				name: keyName.trim(),
				environment: Number(environment),
				scopes: preset ? [...preset.scopes] : [],
			},
			{
				onSuccess: (data) => {
					if (!data.plaintextKey) {
						setError(
							"The server did not return the key secret. Check the key list and revoke this key before trying again.",
						);
						return;
					}
					toast.success(`Key "${keyName.trim()}" created`);
					setPlaintextKey(data.plaintextKey);
				},
				onError: (cause) => setError(failureText("create the key", cause)),
			},
		);
	}

	async function handleCopy() {
		if (!plaintextKey) return;
		try {
			await navigator.clipboard.writeText(plaintextKey);
			// Stays true once set — gates the Done button below. Resetting
			// it on a timeout (the old behaviour) would re-disable Done a
			// few seconds later, which is the wrong UX: once the operator
			// has the secret, that's done forever.
			setCopied(true);
			toast.success("Key copied to clipboard");
		} catch {
			toast.error(
				"Copy failed — select and save the key manually, then confirm below.",
			);
		}
	}

	function handleClose() {
		reset();
		setOpen(false);
	}

	// The footer is this dialog's way out, so the kit renders it in place of its
	// close button. It is briefly disabled while the key is being created, and
	// until the secret is confirmed saved — the checkbox above it is what
	// confirms that, and it is always operable.
	const closeAffordance = plaintextKey ? (
		<DialogFooter>
			<Button onClick={handleClose} disabled={!copied}>
				{copied ? "Done" : "Copy the key first"}
			</Button>
		</DialogFooter>
	) : (
		<DialogFooter>
			<Button
				type="button"
				variant="outline"
				onClick={handleClose}
				disabled={createKey.isPending}
			>
				Cancel
			</Button>
			<Button
				type="submit"
				form={formId}
				disabled={createKey.isPending || !orgId}
			>
				{createKey.isPending ? "Creating..." : "Create key"}
			</Button>
		</DialogFooter>
	);

	return (
		<Dialog
			open={open}
			onOpenChange={(v) => {
				if (!v && (createKey.isPending || (plaintextKey && !copied))) return;
				if (!v) handleClose();
				else setOpen(true);
			}}
		>
			<DialogTrigger render={<Button />}>
				<Plus className="mr-2 h-4 w-4" />
				Create key
			</DialogTrigger>
			<DialogContent
				className="sm:max-w-md"
				showCloseButton={false}
				escape={closeAffordance}
			>
				{plaintextKey ? (
					<>
						<DialogHeader>
							<DialogTitle>API key created</DialogTitle>
							<DialogDescription>
								Copy your API key now. You will not be able to see it again.
							</DialogDescription>
						</DialogHeader>
						<div className="space-y-4 py-4">
							<div className="flex items-center gap-2 rounded-md border bg-muted p-3">
								<code className="flex-1 break-all text-sm font-mono">
									{plaintextKey}
								</code>
								<Button
									variant="ghost"
									size="sm"
									onClick={handleCopy}
									aria-label="Copy API key"
								>
									{copied ? (
										<Check className="h-4 w-4 text-success" />
									) : (
										<Copy className="h-4 w-4" />
									)}
								</Button>
							</div>
							<p className="text-sm text-destructive font-medium">
								This key will not be shown again. Make sure to copy it.
							</p>
							<div className="flex items-center gap-2 text-sm">
								<Checkbox
									id={`${formId}-saved`}
									checked={copied}
									onCheckedChange={setCopied}
								/>
								<Label htmlFor={`${formId}-saved`}>
									I have saved this key securely
								</Label>
							</div>
						</div>
					</>
				) : (
					<form
						id={formId}
						aria-label="Create API key"
						noValidate
						onSubmit={(event) => {
							event.preventDefault();
							handleSubmit();
						}}
						className="space-y-4"
					>
						<DialogHeader>
							<DialogTitle>Create API key</DialogTitle>
							<DialogDescription>
								Let an integration access this organization. Choose only the
								access it needs.
							</DialogDescription>
						</DialogHeader>
						{!orgId && (
							<div className="space-y-2">
								<p>Select an organization to create its API key.</p>
								<OrgSelector />
							</div>
						)}
						{error && <ErrorState title="Key not created" detail={error} />}
						<div className="space-y-4 py-4">
							<Field label="Name" required error={nameError}>
								{(control) => (
									<Input
										{...control}
										disabled={createKey.isPending}
										placeholder="e.g. reporting integration"
										value={keyName}
										onChange={(event) => {
											setKeyName(event.target.value);
											setNameError(undefined);
										}}
									/>
								)}
							</Field>

							<Field label="Environment">
								{(control) => (
									<Select
										disabled={createKey.isPending}
										items={[
											{ value: "1", label: "Live" },
											{ value: "2", label: "Test" },
										]}
										value={environment}
										onValueChange={(v) => {
											if (v) setEnvironment(v);
										}}
									>
										<SelectTrigger {...control}>
											<SelectValue />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="1">Live</SelectItem>
											<SelectItem value="2">Test</SelectItem>
										</SelectContent>
									</Select>
								)}
							</Field>

							<div className="space-y-2">
								<Label id={`${formId}-access-label`}>Access</Label>
								<RadioGroup
									value={scopePreset}
									onValueChange={setScopePreset}
									disabled={createKey.isPending}
									aria-labelledby={`${formId}-access-label`}
									className="rounded-md border divide-y gap-0"
								>
									{SCOPE_PRESETS.map((preset) => (
										<div key={preset.id} className="flex items-start gap-3 p-3">
											<Radio
												id={`${formId}-${preset.id}`}
												value={preset.id}
												aria-describedby={`${formId}-${preset.id}-description`}
											/>
											<div className="space-y-0.5">
												<Label htmlFor={`${formId}-${preset.id}`}>
													{preset.label}
												</Label>
												<p
													id={`${formId}-${preset.id}-description`}
													className="text-xs text-muted-foreground"
												>
													{preset.description}
												</p>
											</div>
										</div>
									))}
								</RadioGroup>
								<p className="text-xs text-muted-foreground">
									Choose the minimum access your integration needs.
								</p>
							</div>
						</div>
					</form>
				)}
			</DialogContent>
		</Dialog>
	);
}
