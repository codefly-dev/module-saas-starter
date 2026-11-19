"use client";

import { DescriptionList } from "@codefly-dev/ui/layout";
import type { CollectionReadGrant } from "@/gen/saas/accounts/v1/authorization_pb";
import { Badge, Section, Stack } from "@/shared/ui";
import type { CatalogueNote, FactView, StateFact } from "../model/facts";

/**
 * One cell. A gap reads as its reason in muted italics with the detail on
 * hover, so a reader never mistakes "this platform does not know" for a value.
 */
export function CatalogueFact({ view }: { view: FactView }) {
	if (view.kind === "gap") {
		return (
			<span
				className="text-sm italic text-muted-foreground"
				title={view.detail}
				data-fact="gap"
			>
				{view.label}
			</span>
		);
	}
	if (view.tone) {
		return (
			<Badge tone={view.tone} dot title={view.detail} data-fact="value">
				{view.text}
			</Badge>
		);
	}
	return (
		<span className="text-sm" title={view.detail} data-fact="value">
			{view.text}
		</span>
	);
}

/**
 * One state's facts, stacked and labelled. A state the platform holds nothing
 * for reads as its gaps, so a row shows where an entry stands in the
 * progression and where this platform cannot tell.
 */
export function StateFacts({
	heading,
	facts,
}: {
	heading: string;
	facts: StateFact[];
}) {
	return (
		// The cell is named by its column, so a reader of one row and a test of
		// one row find "what was approved" without counting columns.
		<div data-slot="state-facts" data-heading={heading} className="min-w-36">
			<DescriptionList
				layout="stacked"
				items={facts.map((item) => ({
					key: item.label,
					term: item.label,
					value: <CatalogueFact view={item.view} />,
				}))}
			/>
		</div>
	);
}

/**
 * The teams a grant reaches inside an installation, in the two kinds the server
 * keeps apart: given at this installation, and inherited from a grant above
 * it. Inherited reach is real reach, but it was never given here, so it is
 * never shown as if it had been.
 */
export function TeamGrants({
	granted,
	inherited,
}: {
	granted: CollectionReadGrant[];
	inherited: CollectionReadGrant[];
}) {
	if (granted.length === 0 && inherited.length === 0) {
		return <span className="text-sm text-muted-foreground">No team</span>;
	}
	const group = (label: string, teams: CollectionReadGrant[]) =>
		teams.length > 0 && (
			<div data-slot="team-grants">
				<p className="text-xs text-muted-foreground">{label}</p>
				<div className="flex flex-wrap gap-1">
					{teams.map((team) => (
						<Badge
							key={team.grant?.id}
							variant="secondary"
							title={`${team.roleName} at ${team.grant?.scopePath || "the organization root"}`}
						>
							{team.subjectLabel}
						</Badge>
					))}
				</div>
			</div>
		);
	return (
		<Stack gap={2}>
			{group("Given access here", granted)}
			{group("Access inherited from a wider grant", inherited)}
		</Stack>
	);
}

/** One list of notes under the table: a title, why it is there, and the rows. */
export function CatalogueNotes({
	title,
	description,
	notes,
}: {
	title: string;
	description: string;
	notes: CatalogueNote[];
}) {
	if (notes.length === 0) return null;
	return (
		<Section title={title} description={description}>
			<DescriptionList
				layout="stacked"
				items={notes.map((note) => ({
					key: `${note.fact}:${note.label}:${note.detail}`,
					term: `${note.fact}: ${note.label}`,
					value: note.detail,
				}))}
			/>
		</Section>
	);
}
