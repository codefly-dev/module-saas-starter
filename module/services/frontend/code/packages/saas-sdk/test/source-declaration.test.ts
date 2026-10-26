import { toJson } from "@bufbuild/protobuf";
import { createRouterTransport } from "@connectrpc/connect";
import { expect, it } from "vitest";
import { accounts, DatasourceService } from "../src/index.js";

it("carries a caller's declaration precondition through the maintained source client", async () => {
	const observed: unknown[] = [];
	const transport = createRouterTransport(({ service }) => {
		service(DatasourceService, {
			invokeSourceOperation(request) {
				observed.push(
					toJson(DatasourceService.method.invokeSourceOperation.input, request),
				);
				return { outputJson: "{}" };
			},
		});
	});
	const client = accounts.New(transport).datasource();
	const input = {
		orgId: "11111111-1111-4111-8111-111111111111",
		sourceId: "22222222-2222-4222-8222-222222222222",
		operation: "read_item",
		inputJson: '{"id":"a"}',
		effectId: "first-call",
	};
	const expectedDeclarationDigest = `sha256:${"a".repeat(64)}`;
	await client.invokeSourceOperation({ ...input, expectedDeclarationDigest });
	await client.invokeSourceOperation(input);
	expect(observed).toEqual([
		{ ...input, expectedDeclarationDigest },
		input,
	]);
});
