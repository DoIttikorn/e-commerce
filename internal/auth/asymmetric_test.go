package auth_test

import (
	"encoding/base64"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/DoIttikorn/e-commerce/internal/auth"
)

func keyPair(t *testing.T) (privateB64, publicB64 string) {
	t.Helper()

	private, public, err := auth.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair() error = %v", err)
	}
	return private, public
}

func TestAsymmetricRoundTrip(t *testing.T) {
	private, public := keyPair(t)

	issuer, err := auth.NewIssuer(private, public, time.Hour)
	if err != nil {
		t.Fatalf("NewIssuer() error = %v", err)
	}

	raw, expiresAt, err := issuer.Issue("user-1")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if !expiresAt.After(time.Now()) {
		t.Error("ExpiresAt is not in the future")
	}

	// A different object, holding only the public half, as another service would.
	verifier, err := auth.NewVerifier(public)
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}

	subject, err := verifier.Verify(raw)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if subject != "user-1" {
		t.Errorf("subject = %q, want %q", subject, "user-1")
	}
}

// The whole reason for splitting the key: a service that can check a token must
// not be able to make one.
func TestAVerifierCannotIssue(t *testing.T) {
	_, public := keyPair(t)

	verifier, err := auth.NewVerifier(public)
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}

	if _, _, err := verifier.Issue("attacker"); err == nil {
		t.Fatal("a verifier minted a token; the private key is not supposed to be there")
	}
}

// Algorithm confusion, the attack that exists only once keys are asymmetric.
//
// The public key is public. If a verifier honours the algorithm named in the
// token, an attacker can sign an HS256 token using that public key as the HMAC
// secret, and the verifier will check it against the very same bytes and agree.
// Nothing secret is needed. Pinning the algorithm is what closes it.
func TestVerifyRejectsAnHMACTokenSignedWithThePublicKey(t *testing.T) {
	private, public := keyPair(t)

	verifier, err := auth.NewVerifier(public)
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	// Sanity: a genuine token is accepted, so a rejection below means the
	// attack failed rather than the setup being broken.
	issuer, _ := auth.NewIssuer(private, public, time.Hour)
	genuine, _, _ := issuer.Issue("user-1")
	if _, err := verifier.Verify(genuine); err != nil {
		t.Fatalf("the genuine token was rejected: %v", err)
	}

	// The attacker knows the public key, because it is public.
	publicKeyBytes := rawPublicKey(t, public)

	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "attacker",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	raw, err := forged.SignedString(publicKeyBytes)
	if err != nil {
		t.Fatalf("building the forged token failed: %v", err)
	}

	if subject, err := verifier.Verify(raw); err == nil {
		t.Fatalf("the forged HS256 token was accepted as %q", subject)
	}
}

func TestAsymmetricVerifyRejectsBadTokens(t *testing.T) {
	private, public := keyPair(t)
	issuer, _ := auth.NewIssuer(private, public, time.Hour)
	verifier, _ := auth.NewVerifier(public)

	valid, _, _ := issuer.Issue("user-1")

	// A token from a completely different key pair.
	otherPrivate, otherPublic := keyPair(t)
	otherIssuer, _ := auth.NewIssuer(otherPrivate, otherPublic, time.Hour)
	foreign, _, _ := otherIssuer.Issue("user-1")

	expiredIssuer, _ := auth.NewIssuer(private, public, -time.Hour)
	expired, _, _ := expiredIssuer.Issue("user-1")

	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"not a jwt", "clearly-not-a-token"},
		{"signature altered", valid[:len(valid)-4] + "AAAA"},
		{"another key pair", foreign},
		{"expired", expired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := verifier.Verify(tt.raw); !errors.Is(err, auth.ErrInvalidToken) {
				t.Errorf("Verify() error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestKeyParsingRejectsRubbish(t *testing.T) {
	if _, err := auth.NewVerifier("not base64 at all!!"); err == nil {
		t.Error("a non-base64 key was accepted")
	}
	if _, err := auth.NewVerifier(base64.StdEncoding.EncodeToString([]byte("not a pem block"))); err == nil {
		t.Error("a non-PEM key was accepted")
	}
	private, public := keyPair(t)
	// The halves the wrong way round.
	if _, err := auth.NewIssuer(public, private, time.Hour); err == nil {
		t.Error("a public key was accepted as a private one")
	}
}

// The scheme comes from configuration, never from a token.
func TestSchemeSelection(t *testing.T) {
	private, public := keyPair(t)
	const secret = "a-secret-of-at-least-thirty-two-chars"

	t.Run("keys win over the secret", func(t *testing.T) {
		issuer, err := auth.NewIssuerFrom(secret, private, public, time.Hour)
		if err != nil {
			t.Fatalf("NewIssuerFrom() error = %v", err)
		}
		raw, _, _ := issuer.Issue("user-1")

		// Verifiable with the public key alone, which an HMAC token would not be.
		verifier, _ := auth.NewVerifier(public)
		if _, err := verifier.Verify(raw); err != nil {
			t.Errorf("the issued token is not an EdDSA one: %v", err)
		}
	})

	t.Run("secret alone falls back to HMAC", func(t *testing.T) {
		issuer, err := auth.NewIssuerFrom(secret, "", "", time.Hour)
		if err != nil {
			t.Fatalf("NewIssuerFrom() error = %v", err)
		}
		raw, _, _ := issuer.Issue("user-1")

		verifier, err := auth.NewVerifierFrom(secret, "")
		if err != nil {
			t.Fatalf("NewVerifierFrom() error = %v", err)
		}
		if _, err := verifier.Verify(raw); err != nil {
			t.Errorf("the HMAC round trip failed: %v", err)
		}
	})

	t.Run("half a key pair is refused", func(t *testing.T) {
		if _, err := auth.NewIssuerFrom(secret, private, "", time.Hour); err == nil {
			t.Error("an issuer was built from half a key pair")
		}
	})

	t.Run("nothing configured is refused", func(t *testing.T) {
		if _, err := auth.NewIssuerFrom("", "", "", time.Hour); err == nil {
			t.Error("an issuer was built with no key at all")
		}
		if _, err := auth.NewVerifierFrom("", ""); err == nil {
			t.Error("a verifier was built with no key at all")
		}
	})
}

// rawPublicKey extracts the key bytes an attacker would have.
func rawPublicKey(t *testing.T, publicB64 string) []byte {
	t.Helper()

	der, err := base64.StdEncoding.DecodeString(publicB64)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	block, _ := pem.Decode(der)
	if block == nil {
		t.Fatal("not a PEM block")
	}
	// The PEM body is what a naive verifier would hand to an HMAC check.
	return block.Bytes
}
