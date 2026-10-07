# Policy log protobuf input

`saas/policylog/v1/witness.proto` is the only transport projection of the
authored Go contract in [../code/witness/contract.go](../code/witness/contract.go).
It carries the four operations the transport-independent service exposes, and
nothing else: there is no REST annotation, no Connect binding and no OpenAPI
document, because the endpoint is private to this module and reaches no gateway.

It deliberately takes **no buf dependency**. `google/protobuf/timestamp.proto`
is a well-known type the toolchain already carries, so generation needs no
registry fetch. Validation is not duplicated here either — `Entry.Validate` in
the Go contract is the one authority for identifier limits and the 32-byte
digest, so a protovalidate annotation would be a second place to disagree.

Regenerate from `module/services/policy-log` with Docker running:

```sh
codefly generate proto --proto ./proto --output .. --template policy-log/proto/buf.gen.yaml
```

The companion image pins every plugin and runs `goimports` over the Go it
wrote. Never run `buf generate` directly: it skips that pass, and
`module/tools/generated-pins-gate.mjs` fails on the resulting import shape.
`buf.gen.local.yaml` beside the service manifest exists only so that gate can
read the plugin versions the image runs; it is not an alternative command.
