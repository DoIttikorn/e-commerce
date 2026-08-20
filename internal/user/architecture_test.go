package user_test

import (
	"go/build"
	"strings"
	"testing"
)

// The rule this file enforces is stated in CLAUDE.md and in the package comment:
// the domain core depends on no framework and no driver. It is easy to state and
// easy to break by accident — one convenient import in a hurry — and the damage
// is not visible until the day somebody tries to swap an adapter.
//
// So it is a test rather than a convention.
func TestDomainCoreImportsNoInfrastructure(t *testing.T) {
	forbidden := []struct {
		prefix string
		why    string
	}{
		{"net/http", "the domain must not know a transport; that is handler/'s job"},
		{"go.mongodb.org", "the domain must not know a driver; that is mongodb/'s job"},
		{"google.golang.org/grpc", "the domain must not know a transport; that is gapi/'s job"},
		{"google.golang.org/protobuf", "generated wire types belong to the adapter"},
		{"github.com/go-chi", "the router is an adapter detail"},
		{"github.com/golang-jwt", "token format hides behind the TokenIssuer port"},
		{"golang.org/x/crypto", "hashing hides behind the Hasher port"},
	}

	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}

	for _, imp := range pkg.Imports {
		for _, f := range forbidden {
			if imp == f.prefix || strings.HasPrefix(imp, f.prefix+"/") {
				t.Errorf("internal/user imports %q — %s", imp, f.why)
			}
		}
	}
}
