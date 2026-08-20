package servicetls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/grpc/credentials"
)

// File names, as written by cmd/certgen.
const (
	CAFile         = "ca.pem"
	ServerCertFile = "server.pem"
	ServerKeyFile  = "server-key.pem"
	ClientCertFile = "client.pem"
	ClientKeyFile  = "client-key.pem"
)

// ServerCredentials requires and verifies a client certificate.
//
// RequireAndVerifyClientCert is the whole point. VerifyClientCertIfGiven would
// accept a caller that simply does not present one, which is the same as having
// no authentication while looking like it has some.
func ServerCredentials(dir string) (credentials.TransportCredentials, error) {
	cert, pool, err := load(dir, ServerCertFile, ServerKeyFile)
	if err != nil {
		return nil, err
	}

	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}), nil
}

// ClientCredentials presents a certificate and verifies the server's.
//
// serverName has to match a name in the server's certificate. It is passed
// explicitly because the address dialled may not be that name — a container
// reached at "product" and a test reaching it at "127.0.0.1" want the same
// certificate to be acceptable.
func ClientCredentials(dir, serverName string) (credentials.TransportCredentials, error) {
	cert, pool, err := load(dir, ClientCertFile, ClientKeyFile)
	if err != nil {
		return nil, err
	}

	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS13,
	}), nil
}

func load(dir, certFile, keyFile string) (tls.Certificate, *x509.CertPool, error) {
	if dir == "" {
		return tls.Certificate{}, nil, errors.New("no certificate directory configured")
	}

	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, certFile), filepath.Join(dir, keyFile))
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load %s: %w", certFile, err)
	}

	caPEM, err := os.ReadFile(filepath.Join(dir, CAFile))
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read %s: %w", CAFile, err)
	}

	// A fresh pool, not the system roots: only this CA may issue a certificate
	// the other side will accept. Appending to the system pool would let any
	// public CA vouch for a caller, which is emphatically not the intent.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, fmt.Errorf("%s contains no usable certificate", CAFile)
	}
	return cert, pool, nil
}
