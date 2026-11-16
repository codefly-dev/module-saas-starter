"use client";

import {
	Button,
	Input,
	Label,
	Radio,
	RadioGroup,
	Spinner,
	Textarea,
	useLoadingPhase,
} from "@codefly-dev/ui/layout";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { CollectionField } from "./collection-field.js";
import type {
	CollectionAccessView,
	ConnectSourceInput,
	DatasourceClient,
	DatasourceCredential,
	DatasourceProviderView,
	SourceGovernanceView,
} from "./types.js";

/** How a provider's own form names each way it can authenticate. */
const modes = {
	static_secret: "Secret key",
	org_app: "Installed app",
	none: "No credential",
} as const;

export function useDatasourceCatalog(client: DatasourceClient, orgId: string) {
	return useQuery({
		queryKey: ["datasource-catalog", orgId],
		queryFn: () => client.getCatalog!(),
		enabled: !!client.getCatalog,
		retry: false,
		refetchInterval: 15000,
	});
}

/**
 * The providers a person can connect now. One that takes no new source — a
 * kind kept running for the sources it already has — is not offered at all:
 * why it is closed is the host's business, not the administrator's.
 */
export function connectableProviders(
	catalog: DatasourceProviderView[] | undefined,
): DatasourceProviderView[] {
	return (catalog ?? []).filter(
		(provider) => provider.acceptsNewSources && !provider.unsupportedReason,
	);
}

/**
 * "Connect a source": the providers that take new sources as one choice, and
 * the chosen one's form beneath it.
 */
export function ConnectSource({
	client,
	orgId,
	governance,
	collections,
	initialConnector,
	renderForm,
	onConnected,
}: {
	client: DatasourceClient;
	orgId: string;
	/** Ties every source connected here to a solution's installation. */
	governance?: SourceGovernanceView;
	/** The organization's collections, offered as where the content lands. */
	collections?: CollectionAccessView[] | undefined;
	/** The provider chosen when the section opens, e.g. on a return from an install. */
	initialConnector?: string | undefined;
	/**
	 * A provider's own form, when the caller has one (GitHub's); the generic
	 * form built from the provider's fields otherwise.
	 */
	renderForm?: (
		provider: DatasourceProviderView,
		close: () => void,
	) => ReactNode | undefined;
	onConnected: (
		input: Pick<ConnectSourceInput, "targetCollection" | "boundaryNodeId">,
	) => void;
}) {
	const [selected, setSelected] = useState<string | undefined>(
		initialConnector,
	);
	const catalog = useDatasourceCatalog(client, orgId);
	const loading = useLoadingPhase(!!client.getCatalog && catalog.isPending);
	if (!client.getCatalog || !client.addSource) return null;
	const providers = connectableProviders(catalog.data);
	const provider = providers.find((entry) => entry.connector === selected);
	const close = () => setSelected(undefined);
	return (
		<section aria-label="Connect a source" className="space-y-4">
			<div className="space-y-1">
				<h3 className="type-section-title">Connect a source</h3>
				<p className="type-body text-muted-foreground">
					Choose where the content comes from.
				</p>
			</div>
			{loading.quiet ? null : loading.indicator ? (
				<Spinner label="Loading providers" />
			) : catalog.isError ? (
				<div role="alert" className="flex items-center gap-3 type-body">
					Couldn’t load what can be connected.
					<Button
						variant="outline"
						size="sm"
						onClick={() => void catalog.refetch()}
					>
						Try again
					</Button>
				</div>
			) : providers.length === 0 ? (
				<p className="type-body text-muted-foreground">
					Nothing can be connected right now.
				</p>
			) : (
				<>
					<RadioGroup
						aria-label="Provider"
						value={selected ?? null}
						onValueChange={(value) => setSelected(String(value))}
						className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3"
					>
						{providers.map((entry) => (
							<Label
								key={entry.connector}
								className="cursor-pointer gap-3 rounded-lg border p-3 has-[[data-checked]]:border-primary has-[[data-checked]]:bg-muted/40"
							>
								<Radio value={entry.connector} />
								{entry.displayName}
							</Label>
						))}
					</RadioGroup>
					{provider && (
						<div className="rounded-lg border p-4">
							{renderForm?.(provider, close) ?? (
								<ProviderForm
									key={JSON.stringify(provider)}
									provider={provider}
									client={client}
									orgId={orgId}
									governance={governance}
									collections={collections}
									onCancel={close}
									onConnected={(boundary) => {
										close();
										onConnected(boundary);
									}}
								/>
							)}
						</div>
					)}
				</>
			)}
		</section>
	);
}

function ProviderForm({
	provider,
	client,
	orgId,
	governance,
	collections,
	onCancel,
	onConnected,
}: {
	provider: DatasourceProviderView;
	client: DatasourceClient;
	orgId: string;
	governance?: SourceGovernanceView;
	collections?: CollectionAccessView[] | undefined;
	onCancel: () => void;
	onConnected: (
		input: Pick<ConnectSourceInput, "targetCollection" | "boundaryNodeId">,
	) => void;
}) {
	const fieldId = useId();
	const idFor = (name: string) => `${fieldId}-${name}`;
	const supported = provider.credentialModes.filter(
		(mode): mode is keyof typeof modes => Object.hasOwn(modes, mode),
	);
	const [mode, setMode] = useState(supported[0]);
	const [pending, setPending] = useState(false);
	const [existing, setExisting] = useState<{ nodeId: string; label: string }>({
		nodeId: "",
		label: "",
	});
	const busy = useRef(false);
	const mounted = useRef(false);
	useEffect(() => {
		mounted.current = true;
		return () => {
			mounted.current = false;
		};
	}, []);
	const [error, setError] = useState<string>();
	if (!mode)
		return (
			<p role="status" className="type-body">
				This provider can&apos;t be connected from this page yet.
			</p>
		);
	return (
		<form
			aria-label={`Connect ${provider.displayName}`}
			className="max-w-2xl space-y-4"
			autoComplete="off"
			onSubmit={async (event) => {
				event.preventDefault();
				if (busy.current) return;
				busy.current = true;
				setPending(true);
				setError(undefined);
				const form = event.currentTarget;
				const fieldValue = (name: string) =>
					(form.elements.namedItem(name) as HTMLInputElement | null)?.value ??
					"";
				// Secrets live only in uncontrolled password inputs until this call. Clear
				// those inputs before invoking any code that could throw or remain pending.
				let credential: DatasourceCredential =
					mode === "static_secret"
						? {
								kind: mode,
								secret: fieldValue("secret"),
								webhookSecret: fieldValue("webhookSecret"),
							}
						: { kind: mode };
				for (const input of form.querySelectorAll<HTMLInputElement>(
					'input[type="password"]',
				))
					input.value = "";
				const boundary = existing.nodeId
					? {
							targetCollection: existing.label,
							boundaryNodeId: existing.nodeId,
						}
					: { targetCollection: fieldValue("collection") };
				try {
					await client.addSource!({
						orgId,
						provider,
						...boundary,
						...(governance ? { governance } : {}),
						credential,
						config: Object.fromEntries(
							provider.fields.map((field) => [
								field.key,
								fieldValue(`config:${field.key}`),
							]),
						),
					});
					if (mounted.current) onConnected(boundary);
				} catch {
					// A transport error can echo submitted bytes. Never retain or render it.
					setError(
						"Couldn't connect. Check the details and access, then enter the secret again.",
					);
				} finally {
					credential = { kind: "none" };
					busy.current = false;
					setPending(false);
				}
			}}
		>
			<fieldset disabled={pending} className="space-y-4">
				{provider.fields.map((field) => (
					<div key={field.key} className="space-y-2">
						<Label htmlFor={idFor(field.key)}>
							{field.displayName}
							{!field.required && (
								<span className="text-muted-foreground"> (optional)</span>
							)}
						</Label>
						{field.format === "text" ? (
							<Input
								id={idFor(field.key)}
								name={`config:${field.key}`}
								required={field.required}
								aria-describedby={idFor(`${field.key}-help`)}
							/>
						) : (
							<Textarea
								id={idFor(field.key)}
								name={`config:${field.key}`}
								required={field.required}
								rows={2}
								aria-describedby={idFor(`${field.key}-help`)}
							/>
						)}
						{(field.help || field.format !== "text") && (
							<p
								id={idFor(`${field.key}-help`)}
								className="type-caption-plain text-muted-foreground"
							>
								{field.help}
								{field.format === "lines"
									? " One per line."
									: field.format === "json"
										? " Enter a JSON value."
										: ""}
							</p>
						)}
					</div>
				))}
				{supported.length > 1 && (
					<RadioGroup
						aria-label="How to connect"
						value={mode}
						onValueChange={(value) => setMode(value as keyof typeof modes)}
					>
						{supported.map((value) => (
							<Label key={value}>
								<Radio value={value} />
								{modes[value]}
							</Label>
						))}
					</RadioGroup>
				)}
				{mode === "static_secret" && (
					<div key={mode} className="space-y-4">
						<div className="space-y-2">
							<Label htmlFor={idFor("secret")}>{modes.static_secret}</Label>
							<Input
								id={idFor("secret")}
								name="secret"
								type="password"
								autoComplete="new-password"
								required
								maxLength={4096}
								aria-describedby={idFor("secret-help")}
							/>
							<p
								id={idFor("secret-help")}
								className="type-caption-plain text-muted-foreground"
							>
								Stored encrypted and never shown again.
							</p>
						</div>
						{provider.supportsWebhook && (
							<div className="space-y-2">
								<Label htmlFor={idFor("webhookSecret")}>
									Webhook secret (optional)
								</Label>
								<Input
									id={idFor("webhookSecret")}
									name="webhookSecret"
									type="password"
									autoComplete="new-password"
									maxLength={1024}
								/>
							</div>
						)}
					</div>
				)}
				<CollectionField
					idFor={idFor}
					{...(collections ? { collections } : {})}
					{...(existing.nodeId ? { boundaryNodeId: existing.nodeId } : {})}
					onSelect={(nodeId, label) => setExisting({ nodeId, label })}
					inputProps={{ name: "collection", required: !existing.nodeId }}
				/>
				{error && (
					<p role="alert" className="type-body text-destructive">
						{error}
					</p>
				)}
				<div className="flex flex-wrap justify-end gap-2">
					<Button type="button" variant="outline" onClick={onCancel}>
						Cancel
					</Button>
					<Button type="submit">
						{pending ? "Connecting…" : `Connect ${provider.displayName}`}
					</Button>
				</div>
			</fieldset>
		</form>
	);
}
