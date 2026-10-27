import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	ConnectObjectStorageForm,
	objectStorageInputFrom,
} from "../connect-object-storage-form.js";

afterEach(cleanup);

function fill(label: string, value: string) {
	fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

function fillRequired() {
	fill("Endpoint", "https://store.example.com");
	fill("Region", "us-east-1");
	fill("Bucket", "acme-docs");
	fill("Access key ID", "AKIAEXAMPLE");
	fill("Secret access key", "example-secret");
	fill("Target collection", "Handbook");
}

describe("ConnectObjectStorageForm", () => {
	it("takes key patterns and file types, one pattern per line", async () => {
		const onSubmit = vi.fn();
		render(
			<ConnectObjectStorageForm
				onSubmit={onSubmit}
				onCancel={() => {}}
				isPending={false}
			/>,
		);
		fillRequired();
		fill("Prefix (optional)", "kb/");
		// A pattern may hold a comma; only a line ends one.
		fill("Include keys matching (optional)", "^guides/\n\\.md$\n^v{1,3}/");
		fill("Exclude keys matching (optional)", "^drafts/");
		fill("File types (optional)", ".MD, .pdf");
		fireEvent.click(screen.getByRole("button", { name: "Connect bucket" }));

		await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
		expect(objectStorageInputFrom("org-1", onSubmit.mock.calls[0][0])).toEqual({
			orgId: "org-1",
			endpoint: "https://store.example.com",
			region: "us-east-1",
			bucket: "acme-docs",
			prefix: "kb/",
			includePatterns: ["^guides/", "\\.md$", "^v{1,3}/"],
			excludePatterns: ["^drafts/"],
			fileExtensions: [".md", ".pdf"],
			accessKeyId: "AKIAEXAMPLE",
			secretAccessKey: "example-secret",
			targetCollection: "Handbook",
			boundaryNodeId: undefined,
		});
	});

	it("shows the host's refusal of a pattern under the field it names", () => {
		const refusal =
			'upload.exclude_patterns[0]: "^(?!drafts/)" is not a valid RE2 expression: invalid or unsupported Perl syntax: `(?!`';
		render(
			<ConnectObjectStorageForm
				onSubmit={() => {}}
				onCancel={() => {}}
				isPending={false}
				errorMessage={refusal}
			/>,
		);
		const field = screen.getByLabelText("Exclude keys matching (optional)");
		expect(field.getAttribute("aria-invalid")).toBe("true");
		const described = document.getElementById(
			field.getAttribute("aria-describedby") ?? "",
		);
		expect(described?.textContent).toBe(refusal);
		// Placed once, under its field: not repeated in the form's own slot.
		expect(screen.getAllByText(refusal)).toHaveLength(1);
		expect(
			screen
				.getByLabelText("Include keys matching (optional)")
				.getAttribute("aria-invalid"),
		).toBe("false");
	});

	it("keeps a refusal that names no key-filter field in the form's own slot", () => {
		render(
			<ConnectObjectStorageForm
				onSubmit={() => {}}
				onCancel={() => {}}
				isPending={false}
				errorMessage="the store refused the access key"
			/>,
		);
		expect(screen.getByRole("alert").textContent).toBe(
			"the store refused the access key",
		);
	});
});
