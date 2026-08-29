# `saas_settings`

Schema-agnostic typed-settings runtime owned by the SaaS Starter, mirroring
the Go `pkg/settings` and TypeScript `@codefly/saas-settings` runtimes.

Products define or override their Settings protobuf, generate the normal Python
bindings, and build a concrete typed catalog with `must_string` / `must_bool` /
`must_enum`. The runtime imports no product schema and does not know how
settings are persisted.

```python
import saas_settings as settings
from product.gen.saas.accounts.v1 import user_settings_pb2 as us

theme = settings.must_enum(us.UserSettings(), "appearance.theme", us.THEME_SYSTEM)
codec = settings.JSONCodec(us.UserSettings)

value = theme.get(document)          # default when absent, without materializing
theme.set(document, us.THEME_DARK)   # creates missing parents
theme.clear(document)                # prunes parents emptied by the clear
stored = codec.marshal(document)     # sparse ProtoJSON, proto field names
```

`get` returns the configured default without mutating the document; an explicit
scalar zero (`False`, `""`, `0`, or the zero enum) stays distinct from an unset
field. Restore a default by clearing the field; never persist reset as JSON
`null`.
