import { useState } from "react";
import {
	PrincipalName,
	PrincipalNamesProvider,
	type PrincipalNamesProviderProps,
	usePrincipalDirectory,
} from "../src/index.js";

export default { title: "SaaS UI/Principal names" };

const VIEWER = "00000000-0000-7000-8000-0000000000a1";
const MEMBER = "00000000-0000-7000-8000-0000000000c3";
const STRANGER = "00000000-0000-7000-8000-0000000000ff";
const ORG = "00000000-0000-7000-8000-00000000000a";

const viewerToken = `header.${btoa(JSON.stringify({ iss: "example", sub: VIEWER, org: ORG }))}.signature`;

type DirectoryAnswer = "members" | "loading" | "refused";

function Preview({ answer }: { answer: DirectoryAnswer }) {
	const [binding] = useState<PrincipalNamesProviderProps["binding"]>(() => ({
		apiBase: "/example",
		getAccessToken: () => viewerToken,
		authedFetch: async () => {
			if (answer === "loading") return new Promise<Response>(() => {});
			if (answer === "refused")
				return Response.json(
					{ code: "permission_denied", message: "not a member" },
					{ status: 403 },
				);
			return Response.json({
				members: [
					{ orgId: ORG, userId: VIEWER, userEmail: "jane.doe@example.com" },
					{ orgId: ORG, userId: MEMBER, userEmail: "john.roe@example.com" },
				],
			});
		},
	}));
	return (
		<PrincipalNamesProvider binding={binding}>
			<ul>
				<li>
					Viewer: <PrincipalName principal={VIEWER} />
				</li>
				<li>
					Member: <PrincipalName principal={MEMBER} />
				</li>
				<li>
					Outside the organization: <PrincipalName principal={STRANGER} />
				</li>
			</ul>
			<DirectoryStatus />
		</PrincipalNamesProvider>
	);
}

function DirectoryStatus() {
	const directory = usePrincipalDirectory();
	return (
		<p>
			Directory:{" "}
			{directory.status === "refused" || directory.status === "failed"
				? `${directory.status} (${directory.reason})`
				: directory.status}
		</p>
	);
}

/** The directory answered: "You", a member's label, and an unknown id shortened. */
export const Named = { render: () => <Preview answer="members" /> };
/** The read is in flight: every id but the viewer's is shortened, with the full id in its title. */
export const Loading = { render: () => <Preview answer="loading" /> };
/** The viewer may not read the directory: said once, every other id shortened. */
export const Refused = { render: () => <Preview answer="refused" /> };
