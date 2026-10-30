import {
	type QueryClient,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { z } from "zod";
import { browserStorage } from "@/features/onboarding/application/browser-storage";
import { useAuth } from "@/lib/auth";

const choice = z
	.object({ teamId: z.string(), teamName: z.string(), role: z.string() })
	.strict();
const draft = z
	.object({
		digest: z.string(),
		generation: z.string(),
		choices: z.array(choice),
		authorized: z
			.object({
				operationId: z.string(),
				installationId: z.string().optional(),
				failure: z.string().optional(),
			})
			.strict()
			.optional(),
	})
	.strict();
export type TeamChoices = z.infer<typeof draft>;
export type TeamChoice = z.infer<typeof choice>;

/** The declaration a set of choices was made against. */
export type ChoiceApproval = { digest: string; generation: bigint } | undefined;

/**
 * Whether staged choices still describe the declaration under review. Choices
 * made against another digest or generation are never applied without an
 * explicit confirmation against the current one.
 */
export function choicesMatch(
	value: TeamChoices | null | undefined,
	approval: ChoiceApproval,
) {
	if (!value?.choices.length) return true;
	return (
		value.digest === (approval?.digest ?? "") &&
		value.generation === (approval?.generation.toString() ?? "")
	);
}

const choicesKey = (actor: string, org: string, target: string) =>
	["installation-team-choices", actor, org, target] as const;

function writeChoices(
	client: QueryClient,
	key: readonly string[],
	value: TeamChoices | null,
) {
	client.setQueryData(key, value);
	try {
		const storage = browserStorage("session");
		if (value) storage?.setItem(JSON.stringify(key), JSON.stringify(value));
		else storage?.removeItem(JSON.stringify(key));
	} catch {
		// In-memory choices still survive pending, retry and remount.
	}
}

/** Bind the choices authorized with one Install to the installation it created. */
export function acknowledgeTeamChoices(
	client: QueryClient,
	actor: string,
	org: string,
	target: string,
	operationId: string,
	installationId: string,
) {
	const key = choicesKey(actor, org, target);
	const value = client.getQueryData<TeamChoices | null>(key);
	if (value?.authorized && value.authorized.operationId === operationId)
		writeChoices(client, key, {
			...value,
			authorized: { ...value.authorized, installationId },
		});
}

/**
 * The person's staged team-per-role choices for one solution, kept for this
 * browser session. They are intent, never authority: nothing here grants, and
 * every grant made from them is the authority's GrantRole decision at the time.
 */
export function useInstallationTeamChoices(orgId: string, targetId: string) {
	const { user } = useAuth();
	const client = useQueryClient();
	const key = choicesKey(user?.id ?? "", orgId, targetId);
	const storageKey = JSON.stringify(key);
	const query = useQuery<TeamChoices | null>({
		queryKey: key,
		enabled: Boolean(user?.id && orgId && targetId),
		staleTime: Infinity,
		gcTime: Infinity,
		queryFn: () => {
			try {
				const raw = browserStorage("session")?.getItem(storageKey);
				const parsed = raw ? draft.safeParse(JSON.parse(raw)) : null;
				return parsed?.success ? parsed.data : null;
			} catch {
				return null;
			}
		},
	});
	const save = (value: TeamChoices | null) => writeChoices(client, key, value);
	const current = () => client.getQueryData<TeamChoices | null>(key) ?? null;
	/** Mark the current choices as approved by the Install about to be sent. */
	const authorize = (operationId: string) => {
		const value = current();
		if (value?.choices.length && operationId)
			save({ ...value, authorized: { operationId } });
	};
	return {
		value: query.data ?? null,
		loading: query.isPending && query.fetchStatus !== "idle",
		current,
		save,
		authorize,
	};
}
