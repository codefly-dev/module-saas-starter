package business

// The hop's admission keys (codefly.dev/authority@0.3.4, RegisterAdmissionKey).
//
// The composition's hop signs each call it admits with a key of its own, and
// every module behind it verifies that admission by the public half, which it
// reads from Keys. A run composing the authority module has its hop service
// sign; a cell composes none, and this host's gateway is the hop there. The hop
// presents the public half here under its workload
// credential; this host attests the caller is the hop and publishes the key
// until its lease lapses. The seed never leaves the hop.
//
// accounts runs several replicas behind one Keys read, so a registration is
// a row in the store, judged by the store's clock: whichever replica the hop
// reaches registers it, and every replica publishes the same set.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/codefly-dev/authority/libraries/authority-client/go/stamp"
)

// AdmissionKeyLease is how long a registered admission key stays published
// without being registered again. The hop renews at a quarter of what remains,
// so a key no running hop signs with is withdrawn once it lapses, and a key
// survives a missed renewal or two.
const AdmissionKeyLease = 2 * time.Minute

// MaxAdmissionKeys bounds the keys published at once: a rotation needs two,
// and a few more absorb rotations closer together than one lease.
const MaxAdmissionKeys = 8

// HopModule and HopService name the local composition's hop: the hop service
// of the authority module, the same pair the local authority admits. A run
// that composes the authority module on this host has it sign admissions.
const (
	HopModule  = "authority"
	HopService = "hop"
)

// GatewayHopService is the service account this host's own gateway runs as,
// the hop on a cell that composes no authority module: it signs the admission
// of every module call it forwards. It is the gateway's declared
// spec.service-account.name in its service.codefly.yaml, pinned to that
// manifest by a test, and it runs in this host's own module's namespace.
const GatewayHopService = "auth-gateway"

// admissionKeyActor is the audit actor of a published admission key: the
// system, since the hop's workload is no person; the payload names it.
const admissionKeyActor = "system"

// ErrAdmissionKeyNotTheHop refuses a registration by any workload other than
// a hop: the composed authority module's hop, or this host's own gateway.
var ErrAdmissionKeyNotTheHop = errors.New("only a hop registers an admission key")

// ErrAdmissionKeyMalformed refuses a key that is not a raw Ed25519 public key.
var ErrAdmissionKeyMalformed = errors.New("an admission key is a raw Ed25519 public key")

// ErrAdmissionKeysFull refuses a new key while MaxAdmissionKeys are published;
// a registered key still renews.
var ErrAdmissionKeysFull = errors.New("the published admission keys are at their bound")

// ErrAdmissionKeysUnwired reports a host with no admission key store: it
// publishes none and registers none, and says so.
var ErrAdmissionKeysUnwired = errors.New("this host has no admission key store wired")

// ErrAdmissionKeyProof refuses a registration whose proof does not verify,
// names another credential, key or audience, is stale or ahead of the clock,
// or repeats a nonce already used (stamp.CheckRegistration).
var ErrAdmissionKeyProof = errors.New("the admission key's registration proof is refused")

// ErrAdmissionKeyPinned refuses a key for a hop instance that holds another
// until its registration lapses: one key is pinned per hop instance.
var ErrAdmissionKeyPinned = errors.New("the hop instance holds another admission key")

// AdmissionRegistration is what one registration binds beside its key: the
// hop instance (its pod) the key is pinned to, and the proof's nonce, used
// once until it could no longer be judged fresh.
type AdmissionRegistration struct {
	Instance   string
	Nonce      string
	NonceUntil time.Time
}

// AdmissionKey is one published hop admission key.
type AdmissionKey struct {
	// ID is stamp.KeyID of the key: the id the hop signs under.
	ID        string
	PublicKey ed25519.PublicKey
	// ExpiresAt is when the registration lapses unless renewed.
	ExpiresAt time.Time
}

// AdmissionKeyStore persists registrations. Both methods judge a lease by the
// store's clock, so replicas whose clocks differ publish the same set.
type AdmissionKeyStore interface {
	// RegisterAdmissionKey reserves the registration's nonce and publishes key
	// for lease from now, pinned to the registration's instance, renewing it
	// when that instance already holds it, and answers when it lapses and
	// whether this call began publishing it (false for a renewal). It refuses
	// ErrAdmissionKeyProof for a nonce already used, ErrAdmissionKeyPinned when
	// the instance holds another unlapsed key, and ErrAdmissionKeysFull when max
	// other keys are published and unlapsed.
	RegisterAdmissionKey(ctx context.Context, key AdmissionKey, registration AdmissionRegistration, lease time.Duration, max int) (expires time.Time, published bool, err error)
	// AdmissionKeys are the keys whose lease has not lapsed, by key id.
	AdmissionKeys(ctx context.Context) ([]AdmissionKey, error)
}

// SetAdmissionKeyStore wires the registrations' persistence. Boot wiring.
func (s *Service) SetAdmissionKeyStore(store AdmissionKeyStore) { s.admissionKeys = store }

// RegisterAdmissionKey records the public key the hop signs call admissions
// with and publishes it until the answered time; registering it again renews
// it. The caller is attested by its workload credential, and any workload but
// the hop is refused by name. Each registration carries a fresh proof signed
// by the key it registers over that credential and this host's audience, used
// once; and one key is pinned per hop instance (its pod) until its lease
// lapses, so a captured hop credential cannot replace or add a key for an
// instance that has registered (interface 0.3.4).
func (s *Service) RegisterAdmissionKey(ctx context.Context, credential string, public []byte, proof string) (AdmissionKey, error) {
	reviewed, err := s.reviewWorkloadCredential(ctx, credential)
	if err != nil {
		return AdmissionKey{}, err
	}
	if hops := s.hopWorkloads(); !slices.Contains(hops, reviewed.Namespace+"/"+reviewed.ServiceAccount) {
		return AdmissionKey{}, fmt.Errorf("%w: the caller is %s/%s, not a hop (%s)",
			ErrAdmissionKeyNotTheHop, reviewed.Namespace, reviewed.ServiceAccount, strings.Join(hops, " or "))
	}
	if reviewed.PodName == "" {
		return AdmissionKey{}, fmt.Errorf("%w: the hop's credential names no pod, so no admission key can be pinned to it", ErrAdmissionKeyNotTheHop)
	}
	if len(public) != ed25519.PublicKeySize {
		return AdmissionKey{}, fmt.Errorf("%w: %d bytes, not %d", ErrAdmissionKeyMalformed, len(public), ed25519.PublicKeySize)
	}
	now := time.Now()
	proved, err := stamp.CheckRegistration(proof, public, credential, executionTokenAudience, now)
	if err != nil {
		return AdmissionKey{}, fmt.Errorf("%w: %w", ErrAdmissionKeyProof, err)
	}
	if s.admissionKeys == nil {
		return AdmissionKey{}, ErrAdmissionKeysUnwired
	}
	registration := AdmissionRegistration{Instance: reviewed.PodName, Nonce: proved.Nonce,
		NonceUntil: now.Add(stamp.RegistrationWindow + 2*stamp.Skew)}
	key := AdmissionKey{ID: stamp.KeyID(public), PublicKey: append(ed25519.PublicKey(nil), public...)}
	err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		expires, published, err := s.admissionKeys.RegisterAdmissionKey(ctx, key, registration, AdmissionKeyLease, MaxAdmissionKeys)
		if err != nil {
			return err
		}
		key.ExpiresAt = expires
		if !published {
			return nil
		}
		// A key every module behind the hop now trusts is recorded in the
		// same transaction that publishes it; a renewal is not.
		return s.emitTx(ctx, admissionKeyActor, ActorTypeSystem, EventHopAdmissionKeyPublished, "hop_admission_key", key.ID, "", map[string]any{
			"key_id": key.ID, "workload": reviewed.Namespace + "/" + reviewed.ServiceAccount, "instance": reviewed.PodName, "expires_at": expires.UTC().Format(time.RFC3339),
		})
	})
	if err != nil {
		return AdmissionKey{}, err
	}
	return key, nil
}

// hopWorkloads are the (namespace/ServiceAccount) workloads that may register
// an admission key: the composed authority module's hop, and this host's own
// gateway when the host knows the module it was composed as. Each namespace is
// read through the cell's rule, never written here.
func (s *Service) hopWorkloads() []string {
	hops := []string{s.moduleNamespaces.Namespace(HopModule) + "/" + HopService}
	if s.hostModule != "" {
		hops = append(hops, s.moduleNamespaces.Namespace(s.hostModule)+"/"+GatewayHopService)
	}
	return hops
}

// PublishedAdmissionKeys are the registered keys still within their lease, by
// key id: what Keys publishes in admission_keys, and never among the Work
// Context keys.
func (s *Service) PublishedAdmissionKeys(ctx context.Context) ([]AdmissionKey, error) {
	if s.admissionKeys == nil {
		return nil, ErrAdmissionKeysUnwired
	}
	var keys []AdmissionKey
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		keys, err = s.admissionKeys.AdmissionKeys(ctx)
		return err
	})
	return keys, err
}
