// Package servicetls builds the mutual-TLS credentials the internal gRPC link
// uses, and generates the development certificates behind them.
package servicetls

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// validity is deliberately short. A development certificate that lasts a decade
// is one nobody ever practises replacing.
const validity = 90 * 24 * time.Hour

// renewWithin is how close to expiry an existing set has to be before Generate
// replaces it rather than leaving it alone.
const renewWithin = 7 * 24 * time.Hour

// generated is every file a complete set consists of. A directory holding some
// of them is a half-written set, and is replaced rather than trusted.
var generated = []string{
	"ca.pem", "server.pem", "server-key.pem", "client.pem", "client-key.pem",
}

// Generate writes a CA, a server key pair and a client key pair into dir.
//
// Development only. A real deployment gets certificates from something that can
// rotate and revoke them; this exists so the compose stack and the test suite
// have a CA without anyone committing a private key.
//
// It is idempotent, and that is not a nicety. Running it again mints a *new* CA,
// and a new CA invalidates every certificate the old one signed — so a
// `docker compose up` that restarted the server but not its client left the
// client trusting an authority that no longer existed, and every call failed
// with "certificate signed by unknown authority". Regenerating only when the
// set is missing or nearly expired makes re-running it safe, which is the same
// property kafka.EnsureTopic has and for the same reason.
func Generate(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if usable, err := existingSetIsUsable(dir); err != nil {
		return err
	} else if usable {
		fmt.Printf("certificates in %s are still valid; leaving them alone\n", dir)
		return nil
	}

	caCert, caKey, caPEM, err := makeCA()
	if err != nil {
		return fmt.Errorf("ca: %w", err)
	}
	if err := write(dir, "ca.pem", caPEM, 0o644); err != nil {
		return err
	}

	// The server certificate names every address a client might dial it on:
	// the compose service name from inside the network, and localhost for a
	// test running on the host. A name missing here is a handshake failure
	// that looks like a configuration problem and is a certificate problem.
	serverPEM, serverKeyPEM, err := issue(caCert, caKey, "product", true,
		[]string{"product", "localhost"}, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")})
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	if err := write(dir, "server.pem", serverPEM, 0o644); err != nil {
		return err
	}
	if err := write(dir, "server-key.pem", serverKeyPEM, 0o600); err != nil {
		return err
	}

	// The client certificate identifies the caller. Its common name is what the
	// server sees, and is how "which service is this?" gets an answer that does
	// not depend on the caller being honest about it.
	clientPEM, clientKeyPEM, err := issue(caCert, caKey, "order", false, nil, nil)
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}
	if err := write(dir, "client.pem", clientPEM, 0o644); err != nil {
		return err
	}
	return write(dir, "client-key.pem", clientKeyPEM, 0o600)
}

func makeCA() (*x509.Certificate, ed25519.PrivateKey, []byte, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}

	serial, err := serialNumber()
	if err != nil {
		return nil, nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "e-commerce internal CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(validity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		return nil, nil, nil, err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	return cert, private, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func issue(
	ca *x509.Certificate, caKey ed25519.PrivateKey,
	commonName string, server bool, dnsNames []string, ips []net.IP,
) (certPEM, keyPEM []byte, err error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := serialNumber()
	if err != nil {
		return nil, nil, err
	}

	usage := x509.ExtKeyUsageClientAuth
	if server {
		usage = x509.ExtKeyUsageServerAuth
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca, public, caKey)
	if err != nil {
		return nil, nil, err
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, nil, err
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

func serialNumber() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func write(dir, name string, body []byte, mode os.FileMode) error {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// existingSetIsUsable reports whether dir already holds a complete set that is
// not about to expire.
//
// Anything unreadable, unparseable, or incomplete counts as unusable rather
// than as an error: the caller's job is to produce a working set, and the
// cheapest way to fix a broken one is to write a new one over it.
func existingSetIsUsable(dir string) (bool, error) {
	for _, name := range generated {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, fmt.Errorf("stat %s: %w", name, err)
		}
	}

	// The server certificate is the one that expires first in practice, and a
	// client that trusts a CA whose leaf has expired fails in the same way as
	// one that trusts nothing. Checking it covers the set.
	body, err := os.ReadFile(filepath.Join(dir, "server.pem"))
	if err != nil {
		return false, nil
	}
	block, _ := pem.Decode(body)
	if block == nil {
		return false, nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, nil
	}
	return time.Until(cert.NotAfter) > renewWithin, nil
}
