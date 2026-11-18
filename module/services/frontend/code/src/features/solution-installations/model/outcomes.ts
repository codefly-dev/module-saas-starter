import {
	type Installation,
	InstallationStatus,
} from "@/gen/saas/accounts/v1/installations_pb";

/** What a finished act tells the person, and how loudly. */
export interface ActOutcome {
	kind: "success" | "warning";
	text: string;
}

type Applied = Pick<Installation, "accessProblems" | "status">;

// What the host could not apply comes back in the installation's access
// problems, in its own sentences: the page says so and names Set up for the
// rest, rather than reporting a success the host did not fully grant.
function withProblems(
	lead: string,
	installation: Pick<Installation, "accessProblems">,
): ActOutcome | undefined {
	if (!installation.accessProblems.length) return undefined;
	return {
		kind: "warning",
		text: `${lead}, but not every role's access was applied — grant the rest in Set up. ${installation.accessProblems.join(" ")}`,
	};
}

/** After a reviewed install was accepted. */
export function installOutcome(installation: Applied): ActOutcome {
	return (
		withProblems("Installed", installation) ?? {
			kind: "success",
			text:
				installation.status === InstallationStatus.ACTIVE
					? "Installed. Each role's team holds it now."
					: "Setup is pending. Each role's team receives it once setup is active.",
		}
	);
}

/** After a module page's, or an installed solution's unheld roles', Set up. */
export function setUpOutcome(alias: string, installation: Applied): ActOutcome {
	return (
		withProblems("Set up", installation) ?? {
			kind: "success",
			text: `Access to ${alias} is set up. Each role's team holds it now.`,
		}
	);
}

/** After a failed or pending installation was retried. */
export function retryOutcome(name: string, installation: Applied): ActOutcome {
	return installation.accessProblems.length
		? {
				kind: "warning",
				text: `${name}: not every role's access was applied — grant the rest in Set up. ${installation.accessProblems.join(" ")}`,
			}
		: { kind: "success", text: `${name} installation pending` };
}
