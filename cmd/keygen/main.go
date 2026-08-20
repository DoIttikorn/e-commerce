// Command keygen prints a JWT signing key pair.
//
// The output is meant to be pasted into .env. Only the user service should be
// given JWT_PRIVATE_KEY; every other service needs JWT_PUBLIC_KEY and nothing
// more, which is the entire reason for generating a pair rather than sharing a
// secret.
package main

import (
	"fmt"
	"os"

	"github.com/DoIttikorn/e-commerce/internal/auth"
)

func main() {
	private, public, err := auth.GenerateKeyPair()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generating the key pair failed:", err)
		os.Exit(1)
	}

	fmt.Println("# Ed25519 key pair, base64-encoded PEM.")
	fmt.Println("# Give the private key to the user service only.")
	fmt.Println()
	fmt.Printf("JWT_PRIVATE_KEY=%s\n", private)
	fmt.Printf("JWT_PUBLIC_KEY=%s\n", public)
}
