import { fromBinary } from "@bufbuild/protobuf";
import {
	FileDescriptorSetSchema,
	type DescriptorProto,
} from "@bufbuild/protobuf/wkt";
import { spawnSync } from "node:child_process";
import {
	mkdirSync,
	mkdtempSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";
import { file_saas_accounts_v1_audit } from "../generated/typescript/src/gen/saas/accounts/v1/audit_pb.js";

const sdk = resolve(dirname(fileURLToPath(import.meta.url)), "..");

it("binds both generation steps to the exported contract, ignoring mutable service protos", () => {
	const temp = mkdtempSync(join(tmpdir(), "audit-sdk-generation-"));
	try {
		const fixtureSdk = join(
			temp,
			"module/services/frontend/code/packages/saas-sdk",
		);
		const accounts = join(temp, "module/services/accounts");
		mkdirSync(join(fixtureSdk, "scripts"), { recursive: true });
		mkdirSync(join(fixtureSdk, "generated/typescript/src/gen"), {
			recursive: true,
		});
		mkdirSync(join(accounts, "proto"), { recursive: true });
		writeFileSync(
			join(accounts, "proto/audit.proto"),
			"deliberately incompatible unpublished schema",
		);
		writeFileSync(
			join(fixtureSdk, "scripts/generate.mjs"),
			readFileSync(join(sdk, "scripts/generate.mjs")),
		);
		const log = join(temp, "calls.jsonl");
		writeFileSync(
			join(temp, "codefly"),
			`#!${process.execPath}\nrequire("node:fs").appendFileSync(process.env.GENERATION_CALLS, JSON.stringify({cwd:process.cwd(),args:process.argv.slice(2)})+"\\n");\n`,
			{ mode: 0o755 },
		);
		const result = spawnSync(
			process.execPath,
			[join(fixtureSdk, "scripts/generate.mjs")],
			{
				env: {
					...process.env,
					PATH: `${temp}:${process.env.PATH}`,
					GENERATION_CALLS: log,
				},
				encoding: "utf8",
			},
		);
		expect(result.status, result.stderr).toBe(0);
		const calls = readFileSync(log, "utf8")
			.trim()
			.split("\n")
			.map((line) => JSON.parse(line) as { cwd: string; args: string[] });
		expect(calls).toHaveLength(2);
		const value = (call: (typeof calls)[number], flag: string) =>
			call.args[call.args.indexOf(flag) + 1];
		const exported = resolve(
			calls[0].cwd,
			value(calls[0], "--from").replace("contracts:", ""),
		);
		expect(resolve(calls[1].cwd, value(calls[1], "--proto"))).toBe(
			join(exported, "accounts/connect/proto"),
		);
	} finally {
		rmSync(temp, { recursive: true, force: true });
	}
});

function messageContract(message: DescriptorProto): unknown {
	return {
		name: message.name,
		fields: message.field.map(
			({
				name,
				number,
				type,
				typeName,
				label,
				oneofIndex,
				proto3Optional,
			}) => ({
				name,
				number,
				type,
				typeName,
				label,
				oneofIndex,
				proto3Optional,
			}),
		),
		nested: message.nestedType.map(messageContract),
	};
}

it("generated audit field contracts equal the exported descriptor, not merely its digest label", () => {
	const exported = fromBinary(
		FileDescriptorSetSchema,
		readFileSync(
			resolve(
				sdk,
				"../../../../../contracts/api/accounts/connect/contract.binpb",
			),
		),
	);
	const audit = exported.file.find(
		(file) => file.name === "saas/accounts/v1/audit.proto",
	);
	expect(audit).toBeDefined();
	expect(
		file_saas_accounts_v1_audit.proto.messageType.map(messageContract),
	).toEqual(audit?.messageType.map(messageContract));
});
