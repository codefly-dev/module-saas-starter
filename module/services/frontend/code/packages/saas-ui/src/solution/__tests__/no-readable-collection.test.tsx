import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import {
	COLLECTION_ACCESS_PATH,
	NoReadableCollection,
	viewerAdministersOrganization,
} from "../index.js";

afterEach(cleanup);

function token(claims: Record<string, unknown>): string {
	const body = btoa(JSON.stringify(claims))
		.replace(/\+/g, "-")
		.replace(/\//g, "_")
		.replace(/=+$/, "");
	return `header.${body}.signature`;
}

describe("viewerAdministersOrganization", () => {
	it.each([
		[{ or: "owner" }, true],
		[{ or: "admin" }, true],
		[{ pr: "super_admin", or: "member" }, true],
		[{ or: "member" }, false],
		[{ pr: "support" }, false],
		[{ pr: "billing", or: "member" }, false],
		[{}, false],
	])("%j → %s", (claims, expected) => {
		expect(viewerAdministersOrganization(token(claims))).toBe(expected);
	});

	it.each([null, undefined, "", "opaque", "a.!!!.c"])(
		"is false for an unreadable credential %j",
		(credential) => {
			expect(viewerAdministersOrganization(credential)).toBe(false);
		},
	);
});

describe("NoReadableCollection", () => {
	it("tells a member whom to ask and where the grant is made", () => {
		render(<NoReadableCollection canGrant={false} />);
		expect(
			screen.getByText(
				"You can’t read any collection yet, so there are no documents to show.",
			),
		).toBeTruthy();
		expect(
			screen.getByText(
				/Ask an organization administrator to grant you read access/,
			),
		).toBeTruthy();
		expect(screen.getByText(/Data sources → Collection access/)).toBeTruthy();
		// A member cannot make the grant, so no link sends them to a page that refuses.
		expect(screen.queryByRole("link")).toBeNull();
	});

	it("gives an administrator the link to make the grant", () => {
		render(<NoReadableCollection canGrant subject="documents to answer from" />);
		expect(screen.getByText(/so there are no documents to answer from\./)).toBeTruthy();
		const link = screen.getByRole("link", {
			name: "Grant read access to a collection",
		});
		expect(link.getAttribute("href")).toBe(COLLECTION_ACCESS_PATH);
		expect(
			screen.getByText(/grants no read access; a grant does/),
		).toBeTruthy();
	});
});
