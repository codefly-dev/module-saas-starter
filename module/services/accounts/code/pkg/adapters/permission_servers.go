package adapters

import "accounts/pkg/permissionsplugin"

// PrincipalService and DelegationService share instances between the raw-gRPC
// and Connect listeners. DelegationServer's in-memory mint cache must survive a
// decision on one protocol followed by a wait on the other.
var (
	principalSingleton  = &PrincipalServer{}
	delegationSingleton = &DelegationServer{}
	usageSingleton      = &UsageServer{}
)

func PrincipalSingleton() *PrincipalServer { return principalSingleton }

func DelegationSingleton() *DelegationServer { return delegationSingleton }

func UsageSingleton() *UsageServer { return usageSingleton }

func configurePermissionServerKeys() {
	plugin := permissionsplugin.Default()
	delegationSingleton.SigningSecret = plugin.HMACSecret()     // gitleaks:allow -- runtime key assignment
	delegationSingleton.SigningEd25519Key = plugin.Ed25519Key() // gitleaks:allow -- runtime key assignment
}
