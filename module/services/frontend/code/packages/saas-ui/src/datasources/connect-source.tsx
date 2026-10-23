"use client";

import {
	Button,
	Card,
	Input,
	Label,
	Radio,
	RadioGroup,
	Spinner,
	Textarea,
	useLoadingPhase,
} from "@codefly-dev/ui/layout";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import type {
	ConnectSourceInput,
	DatasourceClient,
	DatasourceCredential,
	DatasourceProviderView,
} from "./types.js";

const modes = {
	none: "No credential",
	org_app: "Organization app",
	static_secret: "Access credential",
} as const;

/** The catalog is read even with no sources; a refused provider remains visible. */
export function ConnectSource({
	client,
	orgId,
	onConnected,
	onBeginAppSetup,
	appSetup,
}: {
	client: DatasourceClient;
	orgId: string;
	onConnected: (
		input: Pick<ConnectSourceInput, "targetCollection" | "boundaryNodeId">,
	) => void;
	onBeginAppSetup?: () => Promise<void>;
	appSetup?: ReactNode;
}) {
	const [selected, setSelected] = useState<string>();
	const catalog = useQuery({
		queryKey: ["datasource-catalog", orgId],
		queryFn: () => client.getCatalog!(),
		enabled: !!client.getCatalog,
		retry: false,
		refetchInterval: 15000,
	});
	const loading = useLoadingPhase(!!client.getCatalog && catalog.isPending);
	if (!client.getCatalog)
		return (
			<p className="type-body text-muted-foreground">
				Provider catalog unavailable with this client.
			</p>
		);
	if (loading.quiet) return null;
	if (loading.indicator) return <Spinner label="Loading providers" />;
	if (catalog.isError)
		return (
			<div role="alert" className="type-body">
				Could not load the provider catalog.{" "}
				<Button variant="outline" onClick={() => void catalog.refetch()}>
					Retry providers
				</Button>
			</div>
		);
	return (
		<section aria-label="Source providers" className="space-y-3">
			{catalog.data?.length === 0 && (
				<p className="type-body">The host reports no source providers.</p>
			)}
			{catalog.data?.map((provider) => (
				<Card key={provider.connector} title={provider.displayName}>
					<p className="type-body text-muted-foreground">
						{provider.description}
					</p>
					{!provider.acceptsNewSources ? (
						<div role="status" className="type-body">
							<p>Not accepting new sources.</p>
							<p>{provider.gap}</p>
						</div>
					) : provider.unsupportedReason ? (
						<p role="status" className="type-body">
							{provider.unsupportedReason}
						</p>
					) : !client.addSource ? (
						<p className="type-body">Connecting requires a newer client.</p>
					) : appSetup && provider.supportsOrganizationAppSetup ? (
						appSetup
					) : selected === provider.connector ? (
						<ProviderForm
							key={JSON.stringify(provider)}
							provider={provider}
							client={client}
							orgId={orgId}
							onCancel={() => setSelected(undefined)}
							onBeginAppSetup={
								provider.supportsOrganizationAppSetup
									? onBeginAppSetup
									: undefined
							}
							onConnected={(boundary) => {
								setSelected(undefined);
								onConnected(boundary);
							}}
						/>
					) : (
						<Button onClick={() => setSelected(provider.connector)}>
							Connect {provider.displayName}
						</Button>
					)}
				</Card>
			))}
		</section>
	);
}

function ProviderForm({
	provider,
	client,
	orgId,
	onCancel,
	onConnected,
	onBeginAppSetup,
}: {
	provider: DatasourceProviderView;
	client: DatasourceClient;
	orgId: string;
	onCancel: () => void;
	onConnected: (
		input: Pick<ConnectSourceInput, "targetCollection" | "boundaryNodeId">,
	) => void;
	onBeginAppSetup?: () => Promise<void>;
}) {
	const supported = provider.credentialModes.filter(
		(mode): mode is keyof typeof modes => Object.hasOwn(modes, mode),
	);
	const [mode, setMode] = useState(supported[0]);
	const [pending, setPending] = useState(false);
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
				This provider&apos;s authentication requires a newer client.
			</p>
		);
	return (
		<form
			aria-label={`Connect ${provider.displayName}`}
			className="space-y-3"
			autoComplete="off"
			onSubmit={async (event) => {
				event.preventDefault();
				if (busy.current) return;
				busy.current = true;
				setPending(true);
				setError(undefined);
				const form = event.currentTarget;
				const read = (name: string) =>
					(form.elements.namedItem(name) as HTMLInputElement | null)?.value ??
					"";
				// Secrets live only in uncontrolled password inputs until this call. Clear
				// those inputs before invoking any code that could throw or remain pending.
				let credential: DatasourceCredential =
					mode === "static_secret"
						? {
								kind: mode,
								secret: read("secret"),
								webhookSecret: read("webhookSecret"),
							}
						: { kind: mode };
				for (const input of form.querySelectorAll<HTMLInputElement>(
					'input[type="password"]',
				))
					input.value = "";
				const boundary = { targetCollection: read("collection") };
				try {
					await client.addSource!({
						orgId,
						provider,
						...boundary,
						credential,
						config: Object.fromEntries(
							provider.fields.map((field) => [
								field.key,
								read(`config:${field.key}`),
							]),
						),
					});
					if (mounted.current) onConnected(boundary);
				} catch {
					// A transport error can echo submitted bytes. Never retain or render it.
					setError(
						"Could not connect the source. Check its fields and access, then enter the credential again to retry.",
					);
				} finally {
					credential = { kind: "none" };
					busy.current = false;
					setPending(false);
				}
			}}
		>
			<fieldset disabled={pending} className="space-y-3">
				{provider.fields.map((field) => (
					<div key={field.key}>
						<Label className="block type-label">
							{field.displayName}
							{field.format === "text" ? (
								<Input name={`config:${field.key}`} required={field.required} />
							) : (
								<Textarea
									name={`config:${field.key}`}
									required={field.required}
								/>
							)}
						</Label>
						<span className="type-caption text-muted-foreground">
							{field.help}
							{field.format === "lines"
								? " Enter one value per line."
								: field.format === "json"
									? " Enter a JSON value."
									: ""}
						</span>
					</div>
				))}
				<Label className="block type-label">
					Target collection
					<Input name="collection" required maxLength={255} />
				</Label>
				<p className="type-caption text-muted-foreground">
					Connecting does not grant read access. Manage collection grants after
					connecting.
				</p>
				<RadioGroup
					aria-label="Authentication"
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
				{mode === "static_secret" && (
					<div key={mode} className="space-y-3">
						<Label className="block type-label">
							Credential
							<Input
								name="secret"
								type="password"
								autoComplete="new-password"
								required
								maxLength={4096}
							/>
						</Label>
						{provider.supportsWebhook && (
							<Label className="block type-label">
								Webhook signing secret (optional)
								<Input
									name="webhookSecret"
									type="password"
									autoComplete="new-password"
									maxLength={1024}
								/>
							</Label>
						)}
					</div>
				)}
				{mode === "org_app" && onBeginAppSetup && (
					<Button
						type="button"
						variant="outline"
						onClick={() => void onBeginAppSetup()}
					>
						Install organization app
					</Button>
				)}
				{error && (
					<p role="alert" className="type-body text-destructive">
						{error}
					</p>
				)}
				<div className="flex gap-2">
					<Button type="submit">
						{pending ? "Connecting…" : "Connect source"}
					</Button>
					<Button type="button" variant="outline" onClick={onCancel}>
						Cancel
					</Button>
				</div>
			</fieldset>
		</form>
	);
}
