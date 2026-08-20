// Command certgen writes the certificates the internal gRPC link uses.
//
// Service-to-service authentication is not the same problem as user
// authentication, and a user's bearer token is the wrong tool for it: there is
// no user involved when the Order service reserves stock, and borrowing
// somebody's token would mean a compromised Order service can act as whichever
// buyer happened to be checking out.
//
// Mutual TLS answers the actual question — which service is calling? — with a
// key neither side can forge, at the transport rather than in application code
// that has to remember to check.
package main

import (
	"fmt"
	"os"

	"github.com/DoIttikorn/e-commerce/internal/servicetls"
)

func main() {
	dir := "certs"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	if err := servicetls.Generate(dir); err != nil {
		fmt.Fprintln(os.Stderr, "generating certificates failed:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote ca.pem, server.pem, server-key.pem, client.pem and client-key.pem to %s\n", dir)
}
