"use client";

import { Disclosure } from "@codefly-dev/ui/layout";
import { Banner, Button, Field, Section, Stack, Textarea } from "@/shared/ui";

/**
 * The approved permissions of every module, as the one document the platform
 * keeps them in. Approving a waiting module and narrowing an approved one are
 * both edits of this document; nothing is added for the operator, because
 * which modules to approve, and how far, is the decision this page exists for.
 */
export function PermissionsEditor({
	text,
	problem,
	current,
	base,
	edited,
	saving,
	refusal,
	onDismissRefusal,
	onChange,
	onSave,
	onDiscard,
}: {
	text: string;
	/** What is wrong with the text as it stands, or null. */
	problem: string | null;
	/** The version in force, and the one these edits were started from. */
	current: bigint;
	base: bigint;
	edited: boolean;
	saving: boolean;
	refusal: string | null;
	onDismissRefusal: () => void;
	onChange: (text: string) => void;
	onSave: () => void;
	onDiscard: () => void;
}) {
	return (
		<Section
			title="Change what is approved"
			description="All approvals are kept in one document. To approve a waiting module, add it with the permissions you allow; to narrow what a module may do, edit its entry. Saving applies within moments, with no restart, to every organization on this platform, and needs your passkey confirmed in the last 15 minutes."
		>
			{base !== current && (
				<Banner tone="warning">
					Version {current.toString()} was saved after you started editing
					version {base.toString()}, so saving will be refused. Discard your
					changes to start from the current version.
				</Banner>
			)}
			{refusal && (
				<Banner tone="danger" title="Not saved" onDismiss={onDismissRefusal}>
					{refusal}
				</Banner>
			)}
			<Field
				label="Approved permissions"
				description="Written as JSON. The format is under Technical details."
				error={problem ?? undefined}
			>
				{(control) => (
					<Textarea
						{...control}
						className="min-h-72 font-mono text-xs"
						spellCheck={false}
						disabled={saving}
						value={text}
						onChange={(event) => onChange(event.target.value)}
					/>
				)}
			</Field>
			<Stack direction="row" gap={2} className="flex-wrap">
				<Button onClick={onSave} disabled={saving || problem !== null}>
					{saving ? "Saving…" : "Save changes"}
				</Button>
				<Button variant="outline" onClick={onDiscard} disabled={!edited || saving}>
					Discard changes
				</Button>
			</Stack>
			<Disclosure title="Technical details">
				The document is the module envelope: <code>revision</code>, which must
				be {(base + BigInt(1)).toString()} for this save, and{" "}
				<code>principals</code>, keyed by module principal, each holding only
				its <code>grant</code>. Any other key is refused. Saving writes the next
				revision fenced on revision {base.toString()}.
			</Disclosure>
		</Section>
	);
}
