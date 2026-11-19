"use client";

import {
	Button,
	Input,
	Label,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Textarea,
} from "@codefly-dev/ui/layout";
import { zodResolver } from "@hookform/resolvers/zod";
import { type ReactNode, useId } from "react";
import { useForm, useWatch } from "react-hook-form";
import { CollectionField } from "./collection-field.js";
import {
	AccessTokenField,
	AppInstallPrompt,
	type CredentialMethod,
	CredentialMethodField,
	credentialMethodFrom,
	fieldErrorClass as errorClass,
	WebhookSecretField,
} from "./credential-mode.js";
import { type ConnectGitHubValues, connectGitHubSchema } from "./schema.js";
import type { CollectionAccessView, GitHubAppRepositoryView } from "./types.js";

interface ConnectGitHubFormProps {
	collections?: CollectionAccessView[];
	readableNodeIds?: string[];
	collectionError?: boolean;
	/**
	 * Sends the browser off to install the App. Absent when the client cannot
	 * drive App onboarding, which drops the App path and leaves the public and
	 * PAT ones.
	 */
	onBeginAppSetup?: () => void;
	/** Repositories a completed setup returned; undefined until one completes. */
	appRepositories?: GitHubAppRepositoryView[];
	/** Which leg of App setup is in flight, if either. */
	appSetupPhase?: "beginning" | "completing";
	appSetupError?: string;
	onSubmit: (values: ConnectGitHubValues) => void;
	onCancel: () => void;
	isPending: boolean;
	errorMessage?: string;
	/** The way to connect selected first; the token unless the caller says. */
	defaultMethod?: CredentialMethod;
}

export function ConnectGitHubForm({
	onSubmit,
	collections,
	readableNodeIds,
	collectionError,
	onBeginAppSetup,
	appRepositories,
	appSetupPhase,
	appSetupError,
	onCancel,
	isPending,
	errorMessage,
	defaultMethod,
}: ConnectGitHubFormProps) {
	// Unique per instance so two mounted forms don't collide on id/htmlFor
	// (breaks label association + a11y when the panel is reused in more than one
	// place).
	const fieldId = useId();
	const idFor = (name: string) => `${fieldId}-${name}`;
	const form = useForm<ConnectGitHubValues>({
		resolver: zodResolver(connectGitHubSchema),
		defaultValues: {
			// Equal choices: the token comes first unless the caller is landing
			// back from installing the app, whose repositories it just granted.
			method: credentialMethodFrom(defaultMethod ?? "pat", !!onBeginAppSetup),
			repo: "",
			boundaryNodeId: "",
			paths: "",
			fileExtensions: "",
			branch: "",
			targetCollection: "",
			accessToken: "",
			webhookSecret: "",
		},
	});
	const { errors } = form.formState;
	const method = useWatch({ control: form.control, name: "method" });
	const repo = useWatch({ control: form.control, name: "repo" });
	const boundaryNodeId = useWatch({
		control: form.control,
		name: "boundaryNodeId",
	});
	const targetCollection = useWatch({
		control: form.control,
		name: "targetCollection",
	});

	// The App path picks its repository from the installation, so until one
	// returns there is nothing below the install button to fill in.
	const awaitingInstall = method === "app" && !appRepositories;
	const repoError = errors.repo && (
		<p id={idFor("repo-error")} className={errorClass}>
			{errors.repo.message}
		</p>
	);

	return (
		<form
			aria-label="Connect GitHub"
			onSubmit={form.handleSubmit((values) => {
				form.setValue("accessToken", "");
				form.setValue("webhookSecret", "");
				onSubmit(values);
			})}
			className="space-y-6"
			noValidate
		>
			<fieldset disabled={isPending} className="space-y-6">
				<Step number={1} title="How to connect">
					<CredentialMethodField
						id={idFor("method")}
						value={method}
						appAvailable={!!onBeginAppSetup}
						onChange={(next) => {
							form.setValue("method", next);
							// The App path can only connect what the installation
							// grants, and the picker renders blank for anything else —
							// leaving a repository typed on another path selected would
							// submit a repository the reader cannot see chosen.
							if (
								next === "app" &&
								!appRepositories?.some(
									(candidate) => candidate.repo === form.getValues("repo"),
								)
							) {
								form.setValue("repo", "");
								// The branch was filled in from the repository, so it
								// describes one that is no longer selected.
								form.setValue("branch", "");
							}
						}}
					/>
					{method === "pat" && (
						<div className="grid gap-4 md:grid-cols-2">
							<AccessTokenField
								id={idFor("token")}
								errorId={idFor("token-error")}
								{...(errors.accessToken?.message
									? { error: errors.accessToken.message }
									: {})}
								inputProps={form.register("accessToken")}
							/>
							<WebhookSecretField
								id={idFor("secret")}
								errorId={idFor("secret-error")}
								{...(errors.webhookSecret?.message
									? { error: errors.webhookSecret.message }
									: {})}
								inputProps={form.register("webhookSecret")}
							/>
						</div>
					)}
					{awaitingInstall && (
						<AppInstallPrompt
							description="You choose the repositories on GitHub, then come back here to finish."
							onBeginAppSetup={onBeginAppSetup!}
							{...(appSetupPhase ? { phase: appSetupPhase } : {})}
							{...(appSetupError ? { error: appSetupError } : {})}
						/>
					)}
				</Step>

				{!awaitingInstall && (
					<>
						<div className="grid gap-6 md:grid-cols-2">
							<Step number={2} title="Which repository">
								{method === "app" ? (
									<div className="space-y-2">
										<Label htmlFor={idFor("repo")}>Repository</Label>
										<Select
											value={repo ?? ""}
											items={[
												{ value: "", label: "Select a repository" },
												...(appRepositories ?? []).map((candidate) => ({
													value: candidate.repo,
													label: candidate.repo,
												})),
											]}
											onValueChange={(value) => {
												form.setValue("repo", value ?? "");
												form.setValue(
													"branch",
													appRepositories?.find(
														(candidate) => candidate.repo === value,
													)?.defaultBranch ?? "",
												);
											}}
										>
											<SelectTrigger id={idFor("repo")} className="w-full">
												<SelectValue />
											</SelectTrigger>
											<SelectContent>
												<SelectItem value="">Select a repository</SelectItem>
												{appRepositories?.map((candidate) => (
													<SelectItem
														key={candidate.repo}
														value={candidate.repo}
														disabled={candidate.alreadyConnected}
													>
														{candidate.repo}
														{candidate.defaultBranch
															? ` · ${candidate.defaultBranch}`
															: ""}
														{candidate.alreadyConnected
															? " · already connected"
															: ""}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
										{appRepositories?.length === 0 ? (
											<p role="status" className={hintClass}>
												The app can&apos;t see any repository yet. Choose some
												for it on GitHub to connect them here.
											</p>
										) : appRepositories?.every(
												(candidate) => candidate.alreadyConnected,
											) ? (
											<p role="status" className={hintClass}>
												Every repository the app can see is already connected.
												Give it another repository on GitHub to connect it here.
											</p>
										) : (
											<p className={hintClass}>
												The repositories the app can see.
											</p>
										)}
										{repoError}
									</div>
								) : (
									<div className="space-y-2">
										<Label htmlFor={idFor("repo")}>Repository</Label>
										<Input
											id={idFor("repo")}
											aria-invalid={!!errors.repo}
											aria-describedby={idFor(
												errors.repo ? "repo-error" : "repo-help",
											)}
											placeholder="owner/name"
											autoComplete="off"
											spellCheck={false}
											{...form.register("repo")}
										/>
										<p id={idFor("repo-help")} className={hintClass}>
											As it appears on GitHub, e.g. acme/handbook.
										</p>
										{repoError}
									</div>
								)}
								<div className="space-y-2">
									<Label htmlFor={idFor("branch")}>Branch</Label>
									<Input
										id={idFor("branch")}
										aria-invalid={!!errors.branch}
										aria-describedby={idFor(
											errors.branch ? "branch-error" : "branch-help",
										)}
										placeholder="main"
										autoComplete="off"
										spellCheck={false}
										{...form.register("branch")}
									/>
									<p id={idFor("branch-help")} className={hintClass}>
										Leave empty for the repository&apos;s default branch.
									</p>
									{errors.branch && (
										<p id={idFor("branch-error")} className={errorClass}>
											{errors.branch.message}
										</p>
									)}
								</div>
							</Step>

							<Step number={3} title="What to include">
								<div className="space-y-2">
									<Label htmlFor={idFor("paths")}>Folders</Label>
									<Textarea
										id={idFor("paths")}
										className="resize-none font-mono"
										rows={2}
										placeholder="docs/"
										aria-describedby={idFor("paths-help")}
										{...form.register("paths")}
									/>
									<p id={idFor("paths-help")} className={hintClass}>
										One per line. Leave empty for the whole repository.
									</p>
								</div>
								<div className="space-y-2">
									<Label htmlFor={idFor("extensions")}>File types</Label>
									<Input
										id={idFor("extensions")}
										placeholder=".md, .mdx"
										aria-invalid={!!errors.fileExtensions}
										aria-describedby={idFor("extensions-help")}
										{...form.register("fileExtensions")}
									/>
									<p id={idFor("extensions-help")} className={hintClass}>
										Separate with commas. Leave empty for every type.{" "}
										<Button
											type="button"
											variant="link"
											className="h-auto p-0 type-caption-plain"
											onClick={() =>
												form.setValue("fileExtensions", ".md", {
													shouldValidate: true,
												})
											}
										>
											Markdown only
										</Button>
									</p>
									{errors.fileExtensions && (
										<p role="alert" className={errorClass}>
											{errors.fileExtensions.message}
										</p>
									)}
								</div>
							</Step>
						</div>
						<Step number={4} title="Where it lands">
							<CollectionField
								idFor={idFor}
								{...(collections ? { collections } : {})}
								{...(readableNodeIds ? { readableNodeIds } : {})}
								{...(collectionError ? { collectionError } : {})}
								{...(boundaryNodeId ? { boundaryNodeId } : {})}
								targetCollection={targetCollection}
								onSelect={(nodeId, label) => {
									form.setValue("boundaryNodeId", nodeId);
									form.setValue("targetCollection", label);
								}}
								inputProps={form.register("targetCollection")}
								{...(errors.targetCollection?.message
									? { error: errors.targetCollection.message }
									: {})}
							/>
						</Step>
					</>
				)}

				{errorMessage && (
					<p role="alert" className={errorClass}>
						{errorMessage}
					</p>
				)}

				<div className="flex flex-wrap justify-end gap-2">
					<Button type="button" onClick={onCancel} variant="outline">
						Cancel
					</Button>
					{!awaitingInstall && (
						<Button type="submit" disabled={isPending} aria-busy={isPending}>
							{isPending ? "Checking access…" : "Connect repository"}
						</Button>
					)}
				</div>
			</fieldset>
		</form>
	);
}

const hintClass = "type-caption-plain text-muted-foreground";

/** One numbered step of the form: a fieldset its fields belong to. */
function Step({
	number,
	title,
	children,
}: {
	number: number;
	title: string;
	children: ReactNode;
}) {
	return (
		<fieldset className="space-y-3">
			<legend className="mb-2 flex items-center gap-2 type-label">
				<span
					aria-hidden="true"
					className="inline-flex size-5 items-center justify-center rounded-full border type-caption-plain"
				>
					{number}
				</span>
				{title}
			</legend>
			{children}
		</fieldset>
	);
}
