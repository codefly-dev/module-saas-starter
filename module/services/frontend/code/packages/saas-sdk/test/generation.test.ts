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

interface GenerationCall {
	cwd: string;
	args: string[];
}

// Drives the package's own npm scripts, not `scripts/generate.mjs` directly:
// the script only generates what an entry point actually invokes, so a manifest
// that never reaches it leaves the second step — the one that re-emits foreign
// descriptors the client step may omit — unreachable from any documented command.
function runPackageScript(script: string): GenerationCall[] {
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
		for (const file of ["scripts/generate.mjs", "package.json"]) {
			writeFileSync(join(fixtureSdk, file), readFileSync(join(sdk, file)));
		}
		const log = join(temp, "calls.jsonl");
		writeFileSync(
			join(temp, "codefly"),
			`#!${process.execPath}\nrequire("node:fs").appendFileSync(process.env.GENERATION_CALLS, JSON.stringify({cwd:process.cwd(),args:process.argv.slice(2)})+"\\n");\n`,
			{ mode: 0o755 },
		);
		const result = spawnSync("npm", ["run", "--silent", script], {
			cwd: fixtureSdk,
			env: {
				...process.env,
				PATH: `${temp}:${process.env.PATH}`,
				GENERATION_CALLS: log,
			},
			encoding: "utf8",
		});
		expect(result.status, result.stderr).toBe(0);
		return readFileSync(log, "utf8")
			.trim()
			.split("\n")
			.map((line) => JSON.parse(line) as GenerationCall);
	} finally {
		rmSync(temp, { recursive: true, force: true });
	}
}

const flag = (call: GenerationCall, name: string) =>
	call.args[call.args.indexOf(name) + 1];

it("binds both generation steps to the exported contract, ignoring mutable service protos", () => {
	const calls = runPackageScript("generate");
	expect(calls).toHaveLength(2);
	expect(calls[0].args.slice(0, 2)).toEqual(["generate", "client"]);
	expect(calls[1].args.slice(0, 2)).toEqual(["generate", "proto"]);
	const exported = resolve(
		calls[0].cwd,
		flag(calls[0], "--from").replace("contracts:", ""),
	);
	expect(resolve(calls[1].cwd, flag(calls[1], "--proto"))).toBe(
		join(exported, "accounts/connect/proto"),
	);
});

// The client step decides which foreign descriptors land on disk and has been
// seen omitting them; this template regenerates the whole import closure, so it
// is what keeps `buf/validate` and `google/api` present. A `generate` that runs
// only the client step reintroduces the dangling-import tree by construction.
it("regenerates the complete import closure through the sdk buf template", () => {
	const calls = runPackageScript("generate");
	expect(flag(calls[1], "--template")).toBe("buf.gen.sdk.yaml");
});

it("refreshes bindings alone without rewriting the vendored contract or facade", () => {
	const calls = runPackageScript("generate:bindings");
	expect(calls).toHaveLength(1);
	expect(calls[0].args.slice(0, 2)).toEqual(["generate", "proto"]);
	expect(calls[0].args).not.toContain("--bindings-only");
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
