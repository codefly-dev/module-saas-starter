package infra_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	ct "github.com/google/certificate-transparency-go"
	cttls "github.com/google/certificate-transparency-go/tls"
	ctx509 "github.com/google/certificate-transparency-go/x509"
	ctx509util "github.com/google/certificate-transparency-go/x509util"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/require"
)

// An in-process Sigstore whose certificates are what the public-good Fulcio
// issues: a Fulcio chain, the issuer extension, an optional source-repository
// extension, and an embedded Signed Certificate Timestamp from a CT log the
// root names — plus a Rekor entry with its signed entry timestamp and an
// RFC 3161 timestamp, so verification runs every check the host requires.
//
// It exists because sigstore-go's VirtualSigstore embeds no SCT and no
// repository extension. The host now requires an SCT from every signer, and a
// fixture that cannot produce one could only be verified by a weaker verifier
// than production's — which is how a verification strength stops being tested.
// Its Rekor log and timestamp authority are VirtualSigstore's; its Fulcio
// authority and CT log are its own, because their private keys are what an SCT
// and a certificate are made with.
type sigstoreWithCT struct {
	*ca.VirtualSigstore

	fulcio          *root.FulcioCertificateAuthority
	intermediateKey *ecdsa.PrivateKey
	ctLogKey        *ecdsa.PrivateKey
}

func newSigstoreWithCT(t *testing.T) *sigstoreWithCT {
	t.Helper()
	virtual, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	rootCertificate, rootKey, err := ca.GenerateRootCa()
	require.NoError(t, err)
	intermediate, intermediateKey, err := ca.GenerateFulcioIntermediate(rootCertificate, rootKey)
	require.NoError(t, err)
	ctLogKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return &sigstoreWithCT{
		VirtualSigstore: virtual,
		fulcio: &root.FulcioCertificateAuthority{
			Root:                rootCertificate,
			Intermediates:       []*x509.Certificate{intermediate},
			ValidityPeriodStart: time.Now().Add(-5 * time.Hour),
			ValidityPeriodEnd:   time.Now().Add(time.Hour),
			URI:                 "https://fulcio.example.test",
		},
		intermediateKey: intermediateKey,
		ctLogKey:        ctLogKey,
	}
}

// FulcioCertificateAuthorities and CTLogs override VirtualSigstore's: this
// fixture's certificates are issued, and their SCTs signed, by its own keys.
func (s *sigstoreWithCT) FulcioCertificateAuthorities() []root.CertificateAuthority {
	return []root.CertificateAuthority{s.fulcio}
}

func (s *sigstoreWithCT) CTLogs() map[string]*root.TransparencyLog {
	logID := publicKeyID(s.ctLogKey.Public())
	return map[string]*root.TransparencyLog{
		hex.EncodeToString(logID[:]): {
			BaseURL:             "https://ctlog.example.test",
			ID:                  logID[:],
			ValidityPeriodStart: time.Now().Add(-time.Hour),
			ValidityPeriodEnd:   time.Now().Add(time.Hour),
			HashFunc:            crypto.SHA256,
			PublicKey:           s.ctLogKey.Public(),
		},
	}
}

// signing is what one certificate claims, and how its SCT is made.
type signing struct {
	// repository is the source-repository extension a workflow's certificate
	// carries; empty for a person's.
	repository string
	// sctKey signs the embedded SCT; nil uses the fixture's own CT log. A key
	// the root does not name is a log the host cannot check.
	sctKey *ecdsa.PrivateKey
	// noSCT issues the certificate with no SCT at all.
	noSCT bool
}

// Sign issues a certificate for identity at issuer — an e-mail SAN for a
// person, a URI SAN for a workflow — and signs payload with it.
func (s *sigstoreWithCT) Sign(t *testing.T, identity, issuer string, payload []byte) verify.SignedEntity {
	t.Helper()
	return s.SignWith(t, identity, issuer, payload, signing{})
}

func (s *sigstoreWithCT) SignWith(t *testing.T, identity, issuer string, payload []byte, how signing) verify.SignedEntity {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leaf := s.issue(t, identity, issuer, leafKey, how)

	digest := sha256.Sum256(payload)
	signature, err := ecdsa.SignASN1(rand.Reader, leafKey, digest[:])
	require.NoError(t, err)
	stamp, err := s.TimestampResponse(signature)
	require.NoError(t, err)

	return &entityWithCT{
		leaf:       leaf,
		signature:  bundle.NewMessageSignature(digest[:], "SHA2_256", signature),
		timestamps: [][]byte{stamp},
		entries:    []*tlog.Entry{s.logEntry(t, leaf, digest[:], signature)},
	}
}

// issue mints the leaf the way a CT-logging CA does: the TBS certificate is
// signed into an SCT first, then the SCT is embedded and the certificate
// issued. A verifier strips the SCT extension and recovers exactly the TBS the
// log signed, so the two certificates must differ in that extension alone.
func (s *sigstoreWithCT) issue(t *testing.T, identity, issuer string, leafKey *ecdsa.PrivateKey, how signing) *x509.Certificate {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	require.NoError(t, err)
	issuerV2, err := asn1.MarshalWithParams(issuer, "utf8")
	require.NoError(t, err)
	extensions := []pkix.Extension{{Id: certificate.OIDIssuerV2, Value: issuerV2}}
	if how.repository != "" {
		repository, err := asn1.MarshalWithParams(how.repository, "utf8")
		require.NoError(t, err)
		extensions = append(extensions, pkix.Extension{Id: certificate.OIDSourceRepositoryURI, Value: repository})
	}
	template := &x509.Certificate{
		SerialNumber:    serial,
		NotBefore:       time.Now().Add(-time.Minute),
		NotAfter:        time.Now().Add(10 * time.Minute),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		ExtraExtensions: extensions,
	}
	if strings.Contains(identity, "://") {
		uri, err := url.Parse(identity)
		require.NoError(t, err)
		template.URIs = []*url.URL{uri}
	} else {
		template.EmailAddresses = []string{identity}
	}
	intermediate := s.fulcio.Intermediates[0]
	if how.noSCT {
		return s.create(t, template, leafKey)
	}

	precertificate := s.create(t, template, leafKey)
	sctKey := s.ctLogKey
	if how.sctKey != nil {
		sctKey = how.sctKey
	}
	unsigned := ct.SignedCertificateTimestamp{
		SCTVersion: ct.V1,
		LogID:      ct.LogID{KeyID: publicKeyID(sctKey.Public())},
		Timestamp:  uint64(time.Now().UnixMilli()), //nolint:gosec // G115: a current Unix time in milliseconds is positive
	}
	input, err := ct.SerializeSCTSignatureInput(unsigned, ct.LogEntry{Leaf: ct.MerkleTreeLeaf{
		Version:  ct.V1,
		LeafType: ct.TimestampedEntryLeafType,
		TimestampedEntry: &ct.TimestampedEntry{
			Timestamp: unsigned.Timestamp,
			EntryType: ct.PrecertLogEntryType,
			PrecertEntry: &ct.PreCert{
				IssuerKeyHash:  sha256.Sum256(intermediate.RawSubjectPublicKeyInfo),
				TBSCertificate: precertificate.RawTBSCertificate,
			},
		},
	}})
	require.NoError(t, err)
	inputDigest := sha256.Sum256(input)
	sctSignature, err := ecdsa.SignASN1(rand.Reader, sctKey, inputDigest[:])
	require.NoError(t, err)
	unsigned.Signature = ct.DigitallySigned{
		Algorithm: cttls.SignatureAndHashAlgorithm{Hash: cttls.SHA256, Signature: cttls.ECDSA},
		Signature: sctSignature,
	}
	list, err := ctx509util.MarshalSCTsIntoSCTList([]*ct.SignedCertificateTimestamp{&unsigned})
	require.NoError(t, err)
	serialized, err := cttls.Marshal(*list)
	require.NoError(t, err)
	octets, err := asn1.Marshal(serialized)
	require.NoError(t, err)
	template.ExtraExtensions = append(template.ExtraExtensions,
		pkix.Extension{Id: asn1.ObjectIdentifier(ctx509.OIDExtensionCTSCT), Value: octets})
	return s.create(t, template, leafKey)
}

func (s *sigstoreWithCT) create(t *testing.T, template *x509.Certificate, leafKey *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, template, s.fulcio.Intermediates[0], leafKey.Public(), s.intermediateKey)
	require.NoError(t, err)
	parsed, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return parsed
}

// logEntry records the signature in the fixture's Rekor as a hashedrekord, with
// a signed entry timestamp the root's Rekor key verifies.
func (s *sigstoreWithCT) logEntry(t *testing.T, leaf *x509.Certificate, digest, signature []byte) *tlog.Entry {
	t.Helper()
	certificatePEM := "-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(leaf.Raw) + "\n-----END CERTIFICATE-----\n"
	// Canonical JSON: keys sorted, no whitespace — the encoding Rekor stores
	// and the signed entry timestamp covers.
	body, err := json.Marshal(map[string]any{
		"apiVersion": "0.0.1",
		"kind":       "hashedrekord",
		"spec": map[string]any{
			"data": map[string]any{"hash": map[string]any{"algorithm": "sha256", "value": hex.EncodeToString(digest)}},
			"signature": map[string]any{
				"content":   base64.StdEncoding.EncodeToString(signature),
				"publicKey": map[string]any{"content": base64.StdEncoding.EncodeToString([]byte(certificatePEM))},
			},
		},
	})
	require.NoError(t, err)
	logID, err := s.RekorLogID()
	require.NoError(t, err)
	logIDBytes, err := hex.DecodeString(logID)
	require.NoError(t, err)
	integrated := time.Now().Unix()
	const logIndex = 1000
	promise, err := s.RekorSignPayload(tlog.RekorPayload{
		Body: base64.StdEncoding.EncodeToString(body), IntegratedTime: integrated, LogIndex: logIndex, LogID: logID,
	})
	require.NoError(t, err)
	entry, err := tlog.NewTlogEntry(&protorekor.TransparencyLogEntry{
		LogIndex:          logIndex,
		LogId:             &protocommon.LogId{KeyId: logIDBytes},
		KindVersion:       &protorekor.KindVersion{Kind: "hashedrekord", Version: "0.0.1"},
		IntegratedTime:    integrated,
		InclusionPromise:  &protorekor.InclusionPromise{SignedEntryTimestamp: promise},
		CanonicalizedBody: body,
	})
	require.NoError(t, err)
	return entry
}

func publicKeyID(public crypto.PublicKey) [32]byte {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		panic(err)
	}
	return sha256.Sum256(der)
}

// entityWithCT is the signed entity a v0.3 bundle presents: a leaf
// certificate, a message signature over the payload's digest, a timestamp,
// and the Rekor entry with its inclusion promise.
type entityWithCT struct {
	leaf       *x509.Certificate
	signature  *bundle.MessageSignature
	timestamps [][]byte
	entries    []*tlog.Entry
}

func (e *entityWithCT) VerificationContent() (verify.VerificationContent, error) {
	return bundle.NewCertificate(e.leaf), nil
}
func (e *entityWithCT) SignatureContent() (verify.SignatureContent, error) { return e.signature, nil }
func (e *entityWithCT) Timestamps() ([][]byte, error)                      { return e.timestamps, nil }
func (e *entityWithCT) TlogEntries() ([]*tlog.Entry, error)                { return e.entries, nil }
func (e *entityWithCT) HasInclusionPromise() bool                          { return true }
func (e *entityWithCT) HasInclusionProof() bool                            { return false }
func (e *entityWithCT) Version() (string, error)                           { return "v0.3", nil }

// The fixture is only evidence if the verifier really checks what it emits, so
// the fixture is held to the host's own verification strength first: an SCT
// from a log the root names is accepted, and a certificate with no SCT, or
// with one from a log the root does not name, is refused — by the SCT check,
// with the signature, the log entry and the timestamps all otherwise sound.
func TestTheFixtureIsVerifiedAtTheHostsStrength(t *testing.T) {
	sigstore := newSigstoreWithCT(t)
	verifier, err := verify.NewVerifier(sigstore,
		verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1), verify.WithSignedCertificateTimestamps(1))
	require.NoError(t, err)
	payload := []byte(`{"generation":1}`)
	digest := sha256.Sum256(payload)
	policy := verify.NewPolicy(verify.WithArtifactDigest("sha256", digest[:]), verify.WithoutIdentitiesUnsafe())

	_, err = verifier.Verify(sigstore.Sign(t, "dev@example.test", "https://accounts.example.test", payload), policy)
	require.NoError(t, err, "the control: a fixture certificate with its SCT must verify")

	_, err = verifier.Verify(sigstore.SignWith(t, "dev@example.test", "https://accounts.example.test", payload,
		signing{noSCT: true}), policy)
	require.ErrorContains(t, err, "SCT", "a certificate no CT log saw must be refused")

	unlisted, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	_, err = verifier.Verify(sigstore.SignWith(t, "dev@example.test", "https://accounts.example.test", payload,
		signing{sctKey: unlisted}), policy)
	require.ErrorContains(t, err, "SCT", "an SCT from a log the root does not name must be refused")
}
