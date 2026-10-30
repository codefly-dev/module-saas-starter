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
});
