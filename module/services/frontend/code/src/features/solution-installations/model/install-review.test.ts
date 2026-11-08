import { clone, create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
	OfferScopeSchema,
	ReviewedOfferSchema,
} from "@/gen/host/installation/v1/installation_pb";
import { ReviewedSolutionInstallSchema } from "@/gen/saas/accounts/v1/installations_pb";
import {
	assertMatchingOffer,
	moduleConsentText,
	offerScopeText,
	operationText,
	roleReach,
	supportingSummary,
} from "./install-review";

describe("installation access review", () => {
	it("distinguishes unrestricted module resource IDs from no attached solution items", () => {
		expect(
			moduleConsentText(
				'{"capabilities":true,"operation_audiences":{"use":{"audience":"records","invoke_scopes":[{"resource_kind":"record","actions":["read"]}]}}}',
			).join(" "),
		).toContain("all items of this type");
		expect(
			offerScopeText(
				create(OfferScopeSchema, {
					resourceKind: "record",
					actions: ["read"],
					attachedOnly: true,
				}),
			),
		).toContain("none are attached yet");
	});
	it("reads Go's null operation lists as empty without hiding declared operations", () => {
		const lines = moduleConsentText(
			JSON.stringify({
				capabilities: true,
				operation_audiences: {
					search: {
						audience: "records",
						invoke_scopes: null,
						lookup_scopes: [{ resource_kind: "document", actions: ["read"] }],
						headless_scopes: null,
					},
				},
			}),
		);
		expect(lines.join(" ")).toContain("Look up information through records");
		expect(lines.join(" ")).not.toContain("Perform operations through");
	});

	it("describes publication separately from declared external event delivery", () => {
		const lines = moduleConsentText(
			'{"capabilities":true,"namespaces":["example"],"external_namespaces":["example"]}',
		);
		expect(lines).toContain("Publish events in namespaces: example.");
		expect(lines).toContain(
			"Namespaces whose declared external events may be delivered to this organization's configured outbound webhook: example.",
		);
		expect(lines.join(" ")).not.toContain("storage");
		expect(moduleConsentText('{"capabilities":true}')).toContain(
			"No additional jobs, event publication, external event delivery, resource types or cross-module operations are requested.",
		);
	});

	it("does not silently hide unknown access axes or malformed consent", () => {
		expect(() =>
			moduleConsentText('{"capabilities":true,"new_grant":["all"]}'),
		).toThrow();
		expect(() => moduleConsentText("not json")).toThrow();
		expect(moduleConsentText('{"capabilities":false}')).toEqual([
			"No host feature access.",
		]);
	});
	it("binds machine access to the exact signed approval and named declaration", () => {
		const approval = {
			targetId: "principal",
			digest: "digest",
			generation: BigInt(2),
			expectedRevision: BigInt(3),
		};
		const review = create(ReviewedSolutionInstallSchema, {
			target: "example",
			installationRole: "reader",
			approval,
		});
		const offer = create(ReviewedOfferSchema, {
			target: "example",
			installationRole: "reader",
			approval,
			roles: [{ name: "reader" }],
		});
		expect(() => assertMatchingOffer(review, offer)).not.toThrow();
		for (const changed of [
			{ targetId: "other" },
			{ digest: "other" },
			{ generation: BigInt(3) },
			{ expectedRevision: BigInt(4) },
		]) {
			const changedOffer = clone(ReviewedOfferSchema, offer);
			Object.assign(changedOffer.approval!, changed);
			expect(() => assertMatchingOffer(review, changedOffer)).toThrow();
		}
		expect(() =>
			assertMatchingOffer(
				review,
				create(ReviewedOfferSchema, { ...offer, roles: [] }),
			),
		).toThrow();
	});
	it("names each fresh supporting module that holds a standing machine role", () => {
		const module = (target: string, installationRole: string, reused = "") => ({
			principal: `module:${target}`,
			target,
			required: true,
			reuseInstallationId: reused,
			installationRole,
		});
		const summary = supportingSummary([
			module("Alpha", "none"),
			module("Bravo", "reader"),
			module("Charlie", "writer", "installed-charlie"),
		]);
		expect(summary.installs).toEqual(["Alpha", "Bravo"]);
		expect(summary.standing).toEqual(["Bravo (reader)"]);
		expect(summary.reused).toEqual(["Charlie"]);
	});
	it("says each face's delegation mode when two faces of one audience differ", () => {
		const face = (mode: string) => ({
			audience: "example-items",
			delegation_mode: mode,
			invoke_scopes: [{ resource_kind: "item", actions: ["list"] }],
		});
		const differing = moduleConsentText(
			JSON.stringify({
				capabilities: true,
				operation_audiences: {
					EXAMPLE__CONNECT: face("module_actor"),
					EXAMPLE__GRPC: face("person"),
				},
			}),
		);
		expect(differing).toContain(
			"Perform operations through example-items (delegation mode module actor): list item; all items of this type.",
		);
		expect(differing).toContain(
			"Perform operations through example-items (delegation mode person): list item; all items of this type.",
		);
		expect(differing.join("\n")).not.toMatch(/Acts at|EXAMPLE__/);
		const same = moduleConsentText(
			JSON.stringify({
				capabilities: true,
				operation_audiences: {
					EXAMPLE__CONNECT: face("person"),
					EXAMPLE__GRPC: face("person"),
				},
			}),
		);
		expect(same).toContain("Acts at example-items: delegation mode person.");
		expect(same).toContain(
			"Perform operations through example-items: list item; all items of this type.",
		);
	});
	it("renders one capability per operation, whatever face serves it", () => {
		expect(operationText("/docs.Documents/ListDocuments")).toBe(
			"list documents",
		);
		expect(operationText("POST /docs.Documents/ListDocuments")).toBe(
			"list documents",
		);
		expect(operationText("GET /pages/{id}")).toBe("get pages");
		const offer = create(ReviewedOfferSchema, {
			target: "Example",
			roles: [
				{
					name: "viewer",
					reach: [
						{
							path: ["Example", "documents"],
							module: "documents",
							via: "binding",
							operations: [
								"/docs.Documents/ListDocuments",
								"POST /docs.Documents/ListDocuments",
							],
						},
					],
				},
			],
		});
		expect(roleReach(offer)).toEqual([
			{
				where: "Through Example, in documents",
				roles: [{ role: "viewer", text: "list documents" }],
			},
		]);
	});
	it("says each confinement in plain words", () => {
		const offer = create(ReviewedOfferSchema, {
			target: "Example",
			roles: ["attached", "installation", "owner"].map((confinement) => ({
				name: confinement,
				reach: [
					{
						path: ["Example"],
						module: "Example",
						via: "role",
						confinement,
						operations: ["GET /pages/{id}"],
					},
				],
			})),
		});
		expect(roleReach(offer)[0].roles).toEqual([
			{ role: "attached", text: "get pages (attached items)" },
			{ role: "installation", text: "get pages (this installation's items)" },
			{ role: "owner", text: "get pages (items they own)" },
		]);
	});
});
