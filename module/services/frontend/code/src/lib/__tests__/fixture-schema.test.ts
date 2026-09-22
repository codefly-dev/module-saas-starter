import yaml from "js-yaml";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { FixtureFileSchema } from "@/lib/fixtures/types";

const moduleFixture = (name: string) =>
	yaml.load(
		readFileSync(
			resolve(__dirname, "../../../../../../fixtures", `${name}.yaml`),
			"utf-8",
		),
	);

describe("FixtureFileSchema", () => {
	it("keeps the pinned user id the module fixture declares", () => {
		// A plain z.object strips unknown keys, so a schema that does not name
		// `id` silently drops it and /api/fixtures serves users the e2e specs
		// cannot match to a seeded principal.
		const parsed = FixtureFileSchema.parse(moduleFixture("dev-admin"));

		const admin = parsed.users.find((u) => u.email === "admin@acme.com");
		expect(admin?.id).toBe("00000000-0000-7000-8000-0000000000a1");
		for (const user of parsed.users) {
			expect(user.id).toMatch(/^[0-9a-f-]{36}$/);
		}
	});

	it("keeps the pinned organization id the module fixture declares", () => {
		// The tenant a module principal grant names is this id; a schema that
		// dropped it would leave the fixture route serving organizations that
		// cannot be matched to the seeded tenant.
		const parsed = FixtureFileSchema.parse(moduleFixture("dev-admin"));

		const acme = parsed.organizations?.find((o) => o.name === "Acme Corp");
		expect(acme?.id).toBe("00000000-0000-7000-8000-0000000000c1");
		for (const org of parsed.organizations ?? []) {
			expect(org.id).toMatch(/^[0-9a-f-]{36}$/);
		}
	});

	it("rejects an organization id that is not a uuid", () => {
		expect(() =>
			FixtureFileSchema.parse({
				users: [],
				organizations: [{ id: "acme", name: "Acme", owner: "owner@example.com" }],
			}),
		).toThrow();
	});

	it("rejects a user id that is not a uuid", () => {
		expect(() =>
			FixtureFileSchema.parse({
				users: [
					{
						id: "dev-admin",
						email: "owner@example.com",
						name: "Owner",
						role: "member",
						provider: "email",
						provider_id: "owner",
					},
				],
			}),
		).toThrow();
	});
});
