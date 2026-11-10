#!/usr/bin/env bash
# Checks the host-only playground journeys that need no solution against a
# running `codefly run service --fixture dev-admin` (see module/PLAYGROUND.md).
# The address is discovered, never typed; the personas are the fixture's own.
#   scripts/playground-check.sh [--naming-scope <scope>]
set -euo pipefail
cd "$(dirname "$0")/.."
scope=()
[[ "${1:-}" == "--naming-scope" ]] && scope=(--naming-scope "$2")
base=$(codefly endpoint saas-starter/frontend "${scope[@]}" --require-up)
fixture=module/fixtures/dev-admin.yaml
org=$(awk '/^organizations:/{o=1} o&&/id:/{print $3; exit}' "$fixture")
fails=0

token() { # fixture persona email -> access token, as the sign-in page does
  local pid
  pid=$(awk -v e="$1" '$1=="email:"{hit=($2==e)} hit&&$1=="provider_id:"{print $2; exit}' "$fixture")
  curl -fsS -X POST "$base/v1/auth/authenticate" -H 'content-type: application/json' \
    -d "{\"provider\":\"email\",\"fixture\":{\"token\":\"$pid\"}}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["accessToken"])'
}
reads() { # email -> basis of each scope node the person may read ("" when none)
  curl -fsS -X POST "$base/saas.accounts.v1.AccessibleScopeService/ListMyAccessibleScopes" \
    -H "Authorization: Bearer $(token "$1")" -H 'content-type: application/json' \
    -d "{\"orgId\":\"$org\",\"resourceType\":\"example-records\",\"action\":\"read\"}" |
    python3 -c 'import sys,json;print(" ".join(s["basis"] for s in json.load(sys.stdin).get("scopes",[])))'
}
expect() { # description email expected-basis
  local got; got=$(reads "$2")
  if [[ "$got" == "$3" ]]; then echo "pass  $1"; else echo "FAIL  $1: got '${got:-nothing}', want '${3:-nothing}'"; fails=1; fi
}

expect "explicit reader (bob, member) reads the collection by grant"     bob@acme.com   ACCESS_BASIS_GRANT
expect "explicit reader (admin@acme.com) reads it by grant"              admin@acme.com ACCESS_BASIS_GRANT
expect "organization admin (alice) holds no grant: reads nothing"        alice@acme.com ""
expect "organization admin (alex) holds no grant: reads nothing"         alex@acme.test ""
expect "member without a grant (carol) reads nothing"                    carol@acme.com ""

# Explicit grant: the collection's owner gives carol the role at the collection,
# access appears; the grant is revoked, access goes. Nothing else changes it.
api() { # email service/method json
  curl -fsS -X POST "$base/saas.accounts.v1.$2" -H "Authorization: Bearer $(token "$1")" \
    -H 'content-type: application/json' -d "$3"
}
field() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }
owner=admin@acme.com
role=$(api $owner PermissionService/ListRoles "{\"orgId\":\"$org\"}" | field "[r['id'] for r in d['roles'] if r['name']=='Collection reader'][0]")
scopepath=$(api $owner PermissionService/ListCollectionAccess "{\"orgId\":\"$org\"}" | field "d['collections'][0]['node']['scopePath']")
carol=$(awk '$1=="-"&&$2=="id:"{id=$3} $1=="email:"&&$2=="carol@acme.com"{print id; exit}' "$fixture")
grant="{\"orgId\":\"$org\",\"subjectId\":\"$carol\",\"subjectKind\":\"SUBJECT_KIND_PRINCIPAL\",\"scopePath\":\"$scopepath\",\"roleId\":\"$role\"}"
granted=
revoke() { [[ -n "$granted" ]] && api $owner PermissionService/RevokeScope "$grant" >/dev/null 2>&1; granted=; }
trap revoke EXIT
api $owner PermissionService/GrantScope "$grant" >/dev/null && granted=1
expect "after an explicit grant, carol reads the collection"            carol@acme.com ACCESS_BASIS_GRANT
revoke
expect "after the grant is revoked, carol reads nothing again"          carol@acme.com ""
trap - EXIT
exit $fails
