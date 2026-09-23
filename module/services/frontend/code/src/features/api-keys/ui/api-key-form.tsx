"use client";

import { ConnectError } from "@connectrpc/connect";
import { Check, Copy, Plus } from "lucide-react";
import { useId, useState } from "react";
import { toast } from "sonner";
import { OrgSelector } from "@/components/org-selector";
import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
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
			"Read users, orgs, audit logs, webhooks, and entitlements. Cannot mutate.",
		scopes: [{ resource: "*", action: "read" }],
	},
	{
		id: "read_write",
		label: "Read & write",
		description:
			"Full app access. Use for backend integrations that need to mutate data.",
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
		id: "no_scopes",
		label: "No scopes",
		description:
			"For sandboxing — every scoped handler will reject the key. Useful for testing scope enforcement.",
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
	const [error, setError] = useState<string | null>(null);

	const createKey = useCreateAPIKey();
	const formId = useId();

	function reset() {
		setKeyName("");
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
			setError("Enter a name for your API key.");
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
				onError: (cause) =>
					setError(
						ConnectError.from(cause).rawMessage ||
							"Couldn't create the key. Please try again.",
					),
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
				disabled={createKey.isPending || !keyName.trim() || !orgId}
			>
				{createKey.isPending ? "Creating..." : "Create Key"}
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
				Create Key
			</DialogTrigger>
			<DialogContent
				className="sm:max-w-md"
				showCloseButton={false}
				escape={closeAffordance}
			>
				{plaintextKey ? (
					<>
						<DialogHeader>
							<DialogTitle>API Key Created</DialogTitle>
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
										<Check className="h-4 w-4 text-green-500" />
									) : (
										<Copy className="h-4 w-4" />
									)}
								</Button>
							</div>
							<p className="text-sm text-destructive font-medium">
								This key will not be shown again. Make sure to copy it.
							</p>
							<label className="flex items-center gap-2 text-sm">
								<input
									type="checkbox"
									checked={copied}
									onChange={(event) => setCopied(event.target.checked)}
								/>
								I have saved this key securely
							</label>
						</div>
					</>
				) : (
					<form
						id={formId}
						aria-label="Create API key"
						onSubmit={(event) => {
							event.preventDefault();
							handleSubmit();
						}}
						className="space-y-4"
					>
						<DialogHeader>
							<DialogTitle>Create API Key</DialogTitle>
							<DialogDescription>
								Generate a new API key for programmatic access.
							</DialogDescription>
						</DialogHeader>
						{!orgId && (
							<div className="space-y-2">
								<p>Select an organization to create its API key.</p>
								<OrgSelector />
							</div>
						)}
						{error && (
							<p role="alert" className="text-sm text-destructive">
								{error}
							</p>
						)}
						<div className="space-y-4 py-4">
							<div className="space-y-2">
								<Label htmlFor="key-name">Name</Label>
								<Input
									id="key-name"
									required
									disabled={createKey.isPending}
									placeholder="e.g. production-backend"
									value={keyName}
									onChange={(e) => setKeyName(e.target.value)}
								/>
							</div>

							<div className="space-y-2">
								<Label>Environment</Label>
								<Select
									items={[
										{ value: "1", label: "Live" },
										{ value: "2", label: "Test" },
									]}
									value={environment}
									onValueChange={(v) => {
										if (v) setEnvironment(v);
									}}
								>
									<SelectTrigger>
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="1">Live</SelectItem>
										<SelectItem value="2">Test</SelectItem>
									</SelectContent>
								</Select>
							</div>

							<div className="space-y-2">
								<Label>Scopes</Label>
								<div className="rounded-md border divide-y">
									{SCOPE_PRESETS.map((preset) => (
										<label
											key={preset.id}
											className="flex items-start gap-3 p-3 cursor-pointer hover:bg-accent/30"
										>
											<input
												type="radio"
												name="scope-preset"
												value={preset.id}
												checked={scopePreset === preset.id}
												onChange={() => setScopePreset(preset.id)}
												className="mt-0.5"
											/>
											<div className="space-y-0.5">
												<div className="font-medium text-sm">
													{preset.label}
												</div>
												<div className="text-xs text-muted-foreground">
													{preset.description}
												</div>
											</div>
										</label>
									))}
								</div>
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
