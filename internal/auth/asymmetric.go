package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// asymmetricMethod is Ed25519.
//
// Chosen over RSA because the keys are 32 bytes rather than a few hundred,
// signing is faster, and there is no key size to get wrong. Every service
// verifies on every request, so the cost of verification is the one that
// compounds.
var asymmetricMethod = jwt.SigningMethodEdDSA

// AsymmetricTokens issues and verifies EdDSA tokens.
//
// It exists because a shared HMAC secret gives every holder the power to mint
// tokens, not just to check them. In one service that is unremarkable; across
// six it means a bug in the least important of them is a way to become any user
// in the most important. Splitting the key means only the issuer can sign.
type AsymmetricTokens struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
	ttl     time.Duration
}

// NewIssuer returns a Tokens that can both sign and verify.
//
// Only the service that authenticates people should have one. Everything else
// gets a Verifier, which cannot sign — a property of the type rather than of a
// convention somebody has to remember.
func NewIssuer(privatePEMBase64, publicPEMBase64 string, ttl time.Duration) (*AsymmetricTokens, error) {
	private, err := parsePrivateKey(privatePEMBase64)
	if err != nil {
		return nil, err
	}
	public, err := parsePublicKey(publicPEMBase64)
	if err != nil {
		return nil, err
	}
	return &AsymmetricTokens{private: private, public: public, ttl: ttl}, nil
}

// NewVerifier returns a Tokens that can only verify.
//
// There is no private key in it, so a service holding one cannot issue a token
// even if its code is compromised. That is the whole reason to bother.
func NewVerifier(publicPEMBase64 string) (*AsymmetricTokens, error) {
	public, err := parsePublicKey(publicPEMBase64)
	if err != nil {
		return nil, err
	}
	return &AsymmetricTokens{public: public}, nil
}

// Issue returns a signed token for subject.
func (t *AsymmetricTokens) Issue(subject string) (string, time.Time, error) {
	if t.private == nil {
		return "", time.Time{}, errors.New("this service holds no signing key and cannot issue tokens")
	}

	now := time.Now().UTC()
	expiresAt := now.Add(t.ttl)

	token := jwt.NewWithClaims(asymmetricMethod, jwt.RegisteredClaims{
		Subject:   subject,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
	})

	signed, err := token.SignedString(t.private)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, expiresAt, nil
}

// Verify checks a token and returns its subject.
//
// WithValidMethods pins the algorithm here exactly as it does for HS256, and
// for the same reason: honouring the algorithm named in the token lets an
// attacker present one signed with "none", or — the trap specific to
// asymmetric keys — an HS256 token signed using the public key as the HMAC
// secret. The public key is public, so that attack needs nothing secret at all.
func (t *AsymmetricTokens) Verify(raw string) (string, error) {
	var claims jwt.RegisteredClaims

	_, err := jwt.ParseWithClaims(raw, &claims,
		func(*jwt.Token) (any, error) { return t.public, nil },
		jwt.WithValidMethods([]string{asymmetricMethod.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidToken, err)
	}
	if claims.Subject == "" {
		return "", fmt.Errorf("%w: no subject", ErrInvalidToken)
	}
	return claims.Subject, nil
}

// GenerateKeyPair returns a new base64-encoded PEM key pair.
//
// Base64 because these end up in environment variables, and a PEM block's
// newlines do not survive most of the ways an environment variable is set.
func GenerateKeyPair() (privateBase64, publicBase64 string, err error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}

	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return "", "", fmt.Errorf("marshal private key: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return "", "", fmt.Errorf("marshal public key: %w", err)
	}

	return encodePEM("PRIVATE KEY", privateDER), encodePEM("PUBLIC KEY", publicDER), nil
}

func encodePEM(blockType string, der []byte) string {
	return base64.StdEncoding.EncodeToString(
		pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}

func parsePrivateKey(encoded string) (ed25519.PrivateKey, error) {
	block, err := decodePEM(encoded)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, want ed25519", parsed)
	}
	return key, nil
}

func parsePublicKey(encoded string) (ed25519.PublicKey, error) {
	block, err := decodePEM(encoded)
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}

	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, want ed25519", parsed)
	}
	return key, nil
}

func decodePEM(encoded string) (*pem.Block, error) {
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("not valid base64: %w", err)
	}

	block, _ := pem.Decode(der)
	if block == nil {
		return nil, errors.New("not a PEM block")
	}
	return block, nil
}
