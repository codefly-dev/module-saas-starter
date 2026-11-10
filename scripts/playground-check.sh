#!/usr/bin/env bash
# Every journey runs as a plain person holding only explicit grants. A journey that
# passes as an administrator proves nothing about its grant path, so the
# administrators here are cases under test (they must get nothing for free); the
# few steps that need one say why.
# Checks the host-only playground journeys that need no solution against a
# running `codefly run service --fixture dev-admin` (see module/PLAYGROUND.md).
# The address is discovered, never typed; the personas are the fixture's own.
#   scripts/playground-check.sh [--naming-scope <scope>]
set -euo pipefail
cd "$(dirname "$0")/.."
scope=()
if [[ "${1:-}" == "--naming-scope" ]]; then
  [[ -n "${2:-}" ]] || { echo "--naming-scope needs a value" >&2; exit 2; }
  scope=(--naming-scope "$2")
fi
base=$(codefly endpoint saas-starter/frontend ${scope[@]+"${scope[@]}"} --require-up)
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

expect "explicit reader (bob, a plain member) reads the collection by grant" bob@acme.com   ACCESS_BASIS_GRANT
expect "member without a grant (carol) reads nothing"                    carol@acme.com ""
expect "organization admin (alice) holds no grant: reads nothing"        alice@acme.com ""
expect "organization admin (alex) holds no grant: reads nothing"         alex@acme.test ""

# Explicit grant: the collection's owner gives carol the role at the collection,
# access appears; the grant is revoked, access goes. Nothing else changes it.
api() { # email service/method json
  curl -fsS -X POST "$base/saas.accounts.v1.$2" -H "Authorization: Bearer $(token "$1")" \
    -H 'content-type: application/json' -d "$3"
}
field() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }
# Granting and revoking is an act of the collection's owner, so these steps run as
# the organization owner (admin@acme.com): that is the one place an admin is the lens.
owner=admin@acme.com
role=$(api $owner PermissionService/ListRoles "{\"orgId\":\"$org\"}" | field "[r['id'] for r in d['roles'] if r['name']=='Collection reader'][0]")
scopepath=$(api $owner PermissionService/ListCollectionAccess "{\"orgId\":\"$org\"}" | field "d['collections'][0]['node']['scopePath']")
carol=$(awk '$1=="-"&&$2=="id:"{id=$3} $1=="email:"&&$2=="carol@acme.com"{print id; exit}' "$fixture")
grant="{\"orgId\":\"$org\",\"subjectId\":\"$carol\",\"subjectKind\":\"SUBJECT_KIND_PRINCIPAL\",\"scopePath\":\"$scopepath\",\"roleId\":\"$role\"}"
granted=
revoke() {
  [[ -n "$granted" ]] || return 0
  if api $owner PermissionService/RevokeScope "$grant" >/dev/null; then granted=
  else echo "FAIL  the grant to carol could not be revoked: revoke it by hand (RevokeScope)" >&2; fails=1; fi
}
trap revoke EXIT
granted=1 # set first: a grant that lands while the reply is lost must still be revoked
api $owner PermissionService/GrantScope "$grant" >/dev/null
expect "after an explicit grant, carol reads the collection"            carol@acme.com ACCESS_BASIS_GRANT
revoke
expect "after the grant is revoked, carol reads nothing again"          carol@acme.com ""
trap - EXIT

# The platform super_admin is also the collection's owner here. With her own grant
# removed she must read nothing: a platform role is not a read. Restored after.
sarah=$(awk '$1=="-"&&$2=="id:"{id=$3} $1=="email:"&&$2=="admin@acme.com"{print id; exit}' "$fixture")
own="{\"orgId\":\"$org\",\"subjectId\":\"$sarah\",\"subjectKind\":\"SUBJECT_KIND_PRINCIPAL\",\"scopePath\":\"$scopepath\",\"roleId\":\"$role\"}"
restore() { api $owner PermissionService/GrantScope "$own" >/dev/null 2>&1 || echo "FAIL  the owner's grant could not be restored: GrantScope it by hand" >&2; }
trap restore EXIT
api $owner PermissionService/RevokeScope "$own" >/dev/null
expect "platform super_admin with no grant reads nothing"               admin@acme.com ""
restore; trap - EXIT
expect "the same super_admin reads it again once granted"               admin@acme.com ACCESS_BASIS_GRANT

# A platform role reads no other organization's audit trail.
globex=$(awk '/^organizations:/{o=1} o&&/id:/{n++; if(n==2){print $3; exit}}' "$fixture")
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$base/saas.accounts.v1.AuditService/QueryAuditLog" \
  -H "Authorization: Bearer $(token $owner)" -H 'content-type: application/json' -d "{\"orgId\":\"$globex\",\"pageSize\":1}")
if [[ "$code" == 403 ]]; then echo "pass  platform super_admin is refused another organization's audit trail"
else echo "FAIL  platform super_admin read another organization's audit trail (HTTP $code)"; fails=1; fi
exit $fails
