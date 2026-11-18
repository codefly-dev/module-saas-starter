import type { DraftChanges } from "./draft";

function names(list: string[]): string {
	if (list.length <= 1) return list.join("");
	return `${list.slice(0, -1).join(", ")} and ${list[list.length - 1]}`;
}

/** The save, as a question that names the modules it touches. */
export function saveQuestion(changes: DraftChanges): string {
	const { approved, revoked, changed } = changes;
	const touched = [...approved, ...revoked, ...changed];
	if (touched.length === 0) return "Save the module permissions?";
	if (touched.length === 1 && approved.length === 1)
		return `Approve the module ${approved[0]}?`;
	if (touched.length === 1 && revoked.length === 1)
		return `Revoke approval for the module ${revoked[0]}?`;
	return `Save permissions for ${names(touched)}?`;
}

/** What the save changes, module by module, and who it reaches. */
export function saveConsequences(changes: DraftChanges): string {
	const { approved, revoked, changed } = changes;
	const said: string[] = [];
	if (approved.length > 0)
		said.push(
			`${names(approved)} will be able to do what you approved, for every organization on this platform.`,
		);
	if (changed.length > 0)
		said.push(
			`What ${names(changed)} may do changes for every organization, from its next action.`,
		);
	if (revoked.length > 0)
		said.push(
			`${names(revoked)} will stop working for every organization within moments, and installations no longer permitted are removed.`,
		);
	if (said.length === 0)
		said.push("No module gains or loses an approval with this save.");
	said.push("This replaces the permissions in force now.");
	return said.join(" ");
}
