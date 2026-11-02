import { toJson } from "@bufbuild/protobuf";
import { Code, createRouterTransport } from "@connectrpc/connect";
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
	expect(observed).toEqual([{ ...input, expectedDeclarationDigest }, input]);
});

it("verifies the exact original invocation through the maintained public client", async () => {
	const input = {
		orgId: "11111111-1111-4111-8111-111111111111",
		sourceId: "22222222-2222-4222-8222-222222222222",
		operation: "read_item",
		inputJson: '{ "id": "a" }',
		effectId: "retained-call",
		expectedDeclarationDigest: `sha256:${"a".repeat(64)}`,
	};
	const transport = createRouterTransport(({ service }) => {
		service(DatasourceService, {
			verifySourceOperationReceipt(request) {
				expect(
					toJson(
						DatasourceService.method.verifySourceOperationReceipt.input,
						request,
					),
				).toEqual(input);
				return {
					outputJson: "{}",
					receipt: {
						effectId: input.effectId,
						status: "committed",
						outputJson: "{}",
					},
				};
			},
		});
	});
	const response = await accounts
		.New(transport)
		.datasource()
		.verifySourceOperationReceipt(input);
	expect(response.receipt?.status).toBe("committed");
	const oldServer = createRouterTransport(({ service }) => {
		service(DatasourceService, {});
	});
	await expect(
		accounts.New(oldServer).datasource().verifySourceOperationReceipt(input),
	).rejects.toMatchObject({ code: Code.Unimplemented });
});
