package business

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ErrModuleUpstreamRefused names an upstream a minting module reported that the
// host will not route to.
var ErrModuleUpstreamRefused = errors.New("module upstream refused")

// RecordModuleUpstream writes the dynamic half of a declared module's
// registration: the origin its runtime reports at mint, once BindExecution has
// proven the caller is the workload the declared presence names (#952: "the
// lease, upstream and manifest on solution_registrations, reported by the
// runtime"). The static half — the declaration, its alias and target — is the
// delivery's and is never touched here; a module whose declaration is not
// applied and live has no registration to complete.
//
// The gateway reaches the recorded origin only over TLS to the SVID the applied
// presence names, so this origin cannot redirect the module's traffic to a
// workload that is not the module. It is still bounded like every registered
// upstream was: https, host and port only, and a composition-local host.
func (s *Service) RecordModuleUpstream(ctx context.Context, authority ModuleWorkContextAuthority, upstream, manifest string) error {
	upstream, manifest = strings.TrimSpace(upstream), strings.TrimSpace(manifest)
	if upstream == "" && manifest == "" {
		return nil
	}
	origin := ""
	if upstream != "" {
		var err error
		if origin, err = moduleUpstreamOrigin(upstream); err != nil {
			return err
		}
	}
	if manifest != "" && !json.Valid([]byte(manifest)) {
		return fmt.Errorf("%w: the reported manifest is not JSON", ErrModuleUpstreamRefused)
	}
	grant, registered, err := s.currentModuleGrantFromSnapshot(ctx, authority.snapshot, authority.PrincipalID)
	if err != nil {
		return err
	}
	if !registered || grant.PresenceBinding == "" {
		return fmt.Errorf("%w: %s has no delivered presence to complete", ErrModuleUpstreamRefused, authority.PrincipalID)
	}
	now := time.Now().UTC()
	return s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		binding, err := s.store.GetSolutionHostBindingForUpdate(ctx, grant.PresenceBinding)
		if err != nil {
			return err
		}
		if binding == nil || binding.Applied == nil || binding.Applied.Removed || binding.Applied.SolutionID == "" {
			return fmt.Errorf("%w: presence %q is not applied", ErrModuleUpstreamRefused, grant.PresenceBinding)
		}
		solutionID := binding.Applied.SolutionID
		current, err := s.store.GetSolutionRegistrationForUpdate(ctx, solutionID)
		if err != nil {
			return err
		}
		if current == nil || current.TombstonedAt != nil || current.Declared == nil || current.Declared.BindingID != grant.PresenceBinding {
			return fmt.Errorf("%w: %q is not declared by presence %q", ErrModuleUpstreamRefused, solutionID, grant.PresenceBinding)
		}
		backendSame := origin == "" || (current.Backend != nil && current.Backend.Upstream == origin && current.Backend.ServiceAlias == solutionID)
		frontendSame := manifest == "" || (current.Frontend != nil && current.Frontend.Manifest == manifest)
		if backendSame && frontendSame {
			return nil
		}
		revision, err := s.store.NextSolutionRegistryRevision(ctx)
		if err != nil {
			return err
		}
		next := *current
		next.Revision = revision
		next.UpdatedAt = now
		if !backendSame {
			half := SolutionBackendHalf{Revision: revision, Upstream: origin, ServiceAlias: solutionID}
			if current.Backend != nil {
				half.ContractVersion = current.Backend.ContractVersion
			}
			next.Backend = &half
		}
		if !frontendSame {
			half := SolutionFrontendHalf{Revision: revision, Manifest: manifest}
			if current.Frontend != nil {
				half.ContractVersion = current.Frontend.ContractVersion
			}
			next.Frontend = &half
		}
		return s.store.SaveSolutionRegistration(ctx, &next)
	})
}

// ModuleReportUpstream records the origin a workload reports with nothing but
// its proven identity: the projected token and the build it runs are bound
// exactly as for a mint, and no installation is required — a solution nobody
// has installed yet must still be reachable for its install step, which is
// delivered to the origin recorded here.
func (s *Service) ModuleReportUpstream(ctx context.Context, prefix, serviceAccountToken, upstream, manifest string) error {
	if strings.TrimSpace(upstream) == "" && strings.TrimSpace(manifest) == "" {
		return nil
	}
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return fenceErr
	}
	defer releaseAuthority()
	snapshot := s.moduleRegistrySnapshot()
	identity, _, err := s.authorizeModuleIdentity(ctx, snapshot, prefix, serviceAccountToken)
	if err != nil {
		return err
	}
	return s.RecordModuleUpstream(ctx, identity, upstream, manifest)
}

// moduleUpstreamOrigin admits an https origin with nothing but a host and a
// port, on a host that is composition-local: loopback or private, a bare
// service name, or a cluster-local name. Anything else is refused by name.
func moduleUpstreamOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" {
		return "", fmt.Errorf("%w: %q is not an absolute URL", ErrModuleUpstreamRefused, raw)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: %q is not https", ErrModuleUpstreamRefused, raw)
	}
	if parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: %q carries more than an origin", ErrModuleUpstreamRefused, raw)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || parsed.Port() == "" {
		return "", fmt.Errorf("%w: %q names no host and port", ErrModuleUpstreamRefused, raw)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() && !ip.IsPrivate() {
			return "", fmt.Errorf("%w: %q is not composition-local", ErrModuleUpstreamRefused, raw)
		}
	} else if strings.Contains(host, ".") {
		labels := strings.Split(strings.TrimSuffix(host, "."), ".")
		clusterLocal := (len(labels) == 3 && labels[2] == "svc") ||
			(len(labels) == 5 && labels[2] == "svc" && labels[3] == "cluster" && labels[4] == "local")
		if !clusterLocal {
			return "", fmt.Errorf("%w: %q is not composition-local", ErrModuleUpstreamRefused, raw)
		}
	}
	return "https://" + net.JoinHostPort(host, parsed.Port()), nil
}
