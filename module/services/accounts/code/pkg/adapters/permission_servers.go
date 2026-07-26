package adapters

import (
	"accounts/pkg/business"
	"accounts/pkg/permissionsplugin"
)

// PrincipalService and DelegationService share instances between the raw-gRPC
// and Connect listeners. DelegationServer's in-memory mint cache must survive a
// decision on one protocol followed by a wait on the other.
var (
	principalSingleton   = &PrincipalServer{}
	delegationSingleton  = &DelegationServer{}
	workContextSingleton = &WorkContextAuthorityServer{}
	usageSingleton       = &UsageServer{}
)

func PrincipalSingleton() *PrincipalServer { return principalSingleton }

func DelegationSingleton() *DelegationServer { return delegationSingleton }

func WorkContextSingleton() *WorkContextAuthorityServer { return workContextSingleton }

func UsageSingleton() *UsageServer { return usageSingleton }

func configurePermissionServerKeys() {
	plugin := permissionsplugin.Default()
	delegationSingleton.SigningSecret = plugin.HMACSecret()     // gitleaks:allow -- runtime key assignment
	delegationSingleton.SigningEd25519Key = plugin.Ed25519Key() // gitleaks:allow -- runtime key assignment
	var authority business.WorkContextAuthorityStore
	if service != nil {
		authority, _ = service.Store().(business.WorkContextAuthorityStore)
	}
	workContextSingleton.Configure(WorkContextAuthorityConfiguration{
		Issuer:     plugin.WorkContextIssuer(),
		KeyID:      plugin.WorkContextKeyID(),
		PrivateKey: plugin.Ed25519Key(),
		Authority:  authority,
	})
}
