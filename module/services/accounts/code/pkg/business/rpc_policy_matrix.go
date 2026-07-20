package business

import (
	"fmt"
	"sort"
	"strings"

	policyv1 "accounts/pkg/gen/saas/policy/v1"
)

// RenderRPCPolicyMatrix renders the descriptor-authoritative policy inventory.
// Only prose descriptions still come from rpcDescriptions.
func RenderRPCPolicyMatrix() []byte {
	policies := RPCPolicies()
	counts := make(map[RPCPolicyTier]int)
	for _, policy := range policies {
		counts[policy.Tier]++
	}

	var b strings.Builder
	b.WriteString("# Accounts RPC authorization matrix\n\n")
	b.WriteString("> Generated from protobuf service descriptors and `saas.policy.v1.method_policy`; only the prose description is joined from `pkg/business/introspection.go`. Do not edit by hand. Run `go generate ./pkg/business` from `module/services/accounts/code`.\n\n")
	fmt.Fprintf(&b, "Inventory: **%d RPCs** across **%d services**. Missing, invalid, or unclassified procedures are denied by both Connect and gRPC interceptors.\n\n", len(policies), serviceCount(policies))
	b.WriteString("The compact tier is retained for compatibility. Guards, resource bindings, audit events, limiter class, and data sensitivity are descriptor-authoritative. Domain handlers may enforce stronger state-dependent rules but may not weaken this floor.\n\n")
	b.WriteString("| Procedure | Shape | REST mapping | Tier | Guards | Permission / scope | Resource bindings | Audit | Idempotency / rate | Sensitivity req → res | Description |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, policy := range policies {
		shape := "unary"
		if policy.Streaming {
			shape = "server stream"
		}
		rest := "—"
		if policy.HTTPMethod != "" || policy.HTTPPath != "" {
			rest = strings.TrimSpace(policy.HTTPMethod + " " + policy.HTTPPath)
		}
		fmt.Fprintf(&b, "| `%s` | %s | `%s` | `%s` | %s | %s | %s | %s | %s | %s | %s |\n",
			policy.FullMethod,
			shape,
			escapeCell(rest),
			policy.Tier,
			escapeCell(renderGuards(policy.MethodPolicy)),
			escapeCell(renderVocabulary(policy.MethodPolicy)),
			escapeCell(renderBindings(policy.MethodPolicy)),
			escapeCell(renderAudit(policy.MethodPolicy)),
			escapeCell(renderExecution(policy.MethodPolicy)),
			escapeCell(renderSensitivity(policy.MethodPolicy)),
			escapeCell(policy.Description),
		)
	}

	b.WriteString("\n## Tier totals\n\n")
	tiers := make([]string, 0, len(counts))
	for tier := range counts {
		tiers = append(tiers, string(tier))
	}
	sort.Strings(tiers)
	for _, tier := range tiers {
		fmt.Fprintf(&b, "- `%s`: %d\n", tier, counts[RPCPolicyTier(tier)])
	}
	return []byte(b.String())
}

func renderGuards(policy *policyv1.MethodPolicy) string {
	if policy == nil {
		return "INVALID"
	}
	guards := []string{
		"exposure=" + enumTail(policy.GetExposure().String(), "EXPOSURE_"),
		"tenant=" + enumTail(policy.GetTenant().String(), "TENANT_REQUIREMENT_"),
	}
	if policy.GetPlatformRole() != policyv1.PlatformRoleRequirement_PLATFORM_ROLE_REQUIREMENT_NONE {
		guards = append(guards, "platform="+enumTail(policy.GetPlatformRole().String(), "PLATFORM_ROLE_REQUIREMENT_"))
	}
	if policy.GetMfa() != policyv1.MFARequirement_MFA_REQUIREMENT_NONE {
		guards = append(guards, "mfa="+enumTail(policy.GetMfa().String(), "MFA_REQUIREMENT_"))
	}
	if policy.GetAuthenticationFactorAttempt() {
		guards = append(guards, "authentication_factor_attempt=true")
	}
	return strings.Join(guards, "; ")
}

func renderVocabulary(policy *policyv1.MethodPolicy) string {
	if policy == nil {
		return "INVALID"
	}
	parts := make([]string, 0, 2)
	if len(policy.GetPermissions()) > 0 {
		parts = append(parts, "perm="+strings.Join(policy.GetPermissions(), ", "))
	}
	if len(policy.GetScopes()) > 0 {
		parts = append(parts, "scope="+strings.Join(policy.GetScopes(), ", "))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, "; ")
}

func renderBindings(policy *policyv1.MethodPolicy) string {
	if policy == nil {
		return "INVALID"
	}
	bindings := make([]string, 0, len(policy.GetResourceBindings()))
	for _, binding := range policy.GetResourceBindings() {
		bindings = append(bindings, fmt.Sprintf("%s → %s/%s",
			binding.GetRequestField(),
			enumTail(binding.GetTarget().String(), "RESOURCE_TARGET_"),
			enumTail(binding.GetLookup().String(), "RESOURCE_LOOKUP_"),
		))
	}
	if len(bindings) == 0 {
		return "—"
	}
	return strings.Join(bindings, ", ")
}

func renderAudit(policy *policyv1.MethodPolicy) string {
	if policy == nil || policy.GetAudit() == nil {
		return "INVALID"
	}
	if policy.GetAudit().GetEmission() == policyv1.AuditEmission_AUDIT_EMISSION_NONE {
		return "—"
	}
	return enumTail(policy.GetAudit().GetEmission().String(), "AUDIT_EMISSION_") + ": " +
		strings.Join(policy.GetAudit().GetEvents(), ", ")
}

func renderExecution(policy *policyv1.MethodPolicy) string {
	if policy == nil {
		return "INVALID"
	}
	return enumTail(policy.GetIdempotency().String(), "IDEMPOTENCY_REQUIREMENT_") + " / " +
		enumTail(policy.GetRateLimit().String(), "RATE_LIMIT_CLASS_")
}

func renderSensitivity(policy *policyv1.MethodPolicy) string {
	if policy == nil {
		return "INVALID"
	}
	return enumTail(policy.GetRequestSensitivity().String(), "SENSITIVITY_") + " → " +
		enumTail(policy.GetResponseSensitivity().String(), "SENSITIVITY_")
}

func enumTail(value, prefix string) string {
	return strings.TrimPrefix(value, prefix)
}

func serviceCount(policies []RPCPolicy) int {
	services := make(map[string]struct{})
	for _, policy := range policies {
		services[policy.Service] = struct{}{}
	}
	return len(services)
}

func escapeCell(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}
