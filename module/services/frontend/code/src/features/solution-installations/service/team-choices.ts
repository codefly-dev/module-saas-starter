import { type QueryClient, useQuery, useQueryClient } from "@tanstack/react-query";
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
		authorized: z.object({ operationId: z.string(), installationId: z.string().optional(), failure: z.string().optional() }).strict().optional(),
	})
	.strict();
export type TeamChoices = z.infer<typeof draft>;
export type TeamChoice = z.infer<typeof choice>;

const choicesKey = (actor: string, org: string, target: string) => ["installation-team-choices", actor, org, target] as const;
function writeChoices(client: QueryClient, key: readonly string[], value: TeamChoices | null) {
 client.setQueryData(key, value);
 try {
  const storage = browserStorage("session");
  if (value) storage?.setItem(JSON.stringify(key), JSON.stringify(value));
  else storage?.removeItem(JSON.stringify(key));
 } catch { /* In-memory choices still survive pending and retry. */ }
}

/** Bind original explicit Install choices only to its acknowledged canonical ID. */
export function acknowledgeTeamChoices(client: QueryClient, actor: string, org: string, target: string, operationId: string, installationId: string) {
 const key = choicesKey(actor, org, target);
 const value = client.getQueryData<TeamChoices | null>(key);
 if (value?.authorized?.operationId === operationId) writeChoices(client, key, {...value, authorized: {...value.authorized, installationId}});
}

/** Session UI intent only; never substitutes for current caller/role authorization. */
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
 const authorize = (operationId: string) => {
  const value = client.getQueryData<TeamChoices | null>(key);
  if (value?.choices.length) save({...value, authorized: {operationId}});
 };
 return { value: query.data ?? null, loading: query.isPending, save, authorize };
}
