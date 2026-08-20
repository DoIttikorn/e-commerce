package integration

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"

	productv1 "github.com/DoIttikorn/e-commerce/api/product/v1"
	"github.com/DoIttikorn/e-commerce/internal/order"
	"github.com/DoIttikorn/e-commerce/internal/order/grpcstock"
	"github.com/DoIttikorn/e-commerce/internal/product"
	productgapi "github.com/DoIttikorn/e-commerce/internal/product/gapi"
	productmongo "github.com/DoIttikorn/e-commerce/internal/product/mongodb"
	"github.com/DoIttikorn/e-commerce/internal/servicetls"
)

// certDir generates a fresh CA and key pairs into a temporary directory.
//
// Per test rather than shared, so nothing here can pass because of a
// certificate somebody left lying around.
func certDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := servicetls.Generate(dir); err != nil {
		t.Fatalf("generating certificates: %v", err)
	}
	return dir
}

type tlsFixture struct {
	addr string
	repo *productmongo.Repository
	item product.Product
}

// startStockServer serves the stock API over mutual TLS on a real port.
func startStockServer(t *testing.T, tlsDir string) (tlsFixture, context.Context) {
	t.Helper()

	db, ctx := mongoFor(t, "product")
	dropAll(t, ctx, db, productmongo.CollectionName, productmongo.ReservationCollectionName)

	repo := productmongo.NewRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	svc := product.NewService(repo, productmongo.NewDirectory(db), discard())

	created, err := repo.Create(ctx, product.Product{
		ID: repo.NextID(), SellerID: "seller-1", SellerName: "TLS Shop", Name: "Mug",
		PriceMinor: 25000, Currency: "THB", Stock: 10, CreatedAt: time.Now().UTC(),
	}, nil)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	creds, err := servicetls.ServerCredentials(tlsDir)
	if err != nil {
		t.Fatalf("server credentials: %v", err)
	}

	srv := grpc.NewServer(grpc.Creds(creds))
	productv1.RegisterStockServiceServer(srv, productgapi.New(svc))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)

	return tlsFixture{addr: listener.Addr().String(), repo: repo, item: created}, ctx
}

func oneLine(productID string, qty int) []order.ReserveLine {
	return []order.ReserveLine{{ProductID: productID, Quantity: qty}}
}

// A caller holding a certificate this CA issued gets through.
func TestMutualTLSAcceptsAKnownClient(t *testing.T) {
	dir := certDir(t)
	fix, ctx := startStockServer(t, dir)

	client, err := grpcstock.Dial(fix.addr, dir, "localhost")
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	reserved, err := client.Reserve(ctx, "mtls-ok", oneLine(fix.item.ID, 2))
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if len(reserved) != 1 || reserved[0].Quantity != 2 {
		t.Errorf("reserved = %+v, want one line of 2", reserved)
	}
}

// The test that matters. Without RequireAndVerifyClientCert this call would
// succeed, and anything able to reach the port could take inventory.
func TestMutualTLSRejectsAClientWithoutACertificate(t *testing.T) {
	dir := certDir(t)
	fix, ctx := startStockServer(t, dir)

	// Plaintext: exactly what an unauthenticated caller would try.
	client, err := grpcstock.Dial(fix.addr, "", "")
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if _, err := client.Reserve(ctx, "mtls-anon", oneLine(fix.item.ID, 1)); err == nil {
		t.Fatal("a caller with no client certificate reserved stock")
	}

	after, err := fix.repo.ByID(ctx, fix.item.ID)
	if err != nil {
		t.Fatalf("ByID() error = %v", err)
	}
	if after.Stock != 10 {
		t.Errorf("stock = %d, want 10 — the rejected call took stock anyway", after.Stock)
	}
}

// A certificate from a different CA is not a certificate from this one.
func TestMutualTLSRejectsACertificateFromAnotherAuthority(t *testing.T) {
	serverDir := certDir(t)
	strangerDir := certDir(t) // an entirely separate CA
	fix, ctx := startStockServer(t, serverDir)

	client, err := grpcstock.Dial(fix.addr, strangerDir, "localhost")
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if _, err := client.Reserve(ctx, "mtls-stranger", oneLine(fix.item.ID, 1)); err == nil {
		t.Fatal("a certificate signed by an unrelated CA was accepted")
	}
}

// The server certificate has to carry the name the client dials it by.
func TestMutualTLSRejectsAMismatchedServerName(t *testing.T) {
	dir := certDir(t)
	fix, ctx := startStockServer(t, dir)

	client, err := grpcstock.Dial(fix.addr, dir, "not-in-the-certificate")
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if _, err := client.Reserve(ctx, "mtls-badname", oneLine(fix.item.ID, 1)); err == nil {
		t.Fatal("a server whose certificate does not name it was accepted")
	}
}

// Re-running the generator must not invalidate the certificates already in use.
//
// A new CA invalidates everything the old one signed. When certgen ran on every
// `docker compose up`, a partial restart left the Order service trusting an
// authority that no longer existed, and every stock reservation failed with
// "certificate signed by unknown authority" — with both containers healthy and
// nothing in either log to suggest a certificate problem until the handshake.
func TestGeneratingTwiceLeavesTheCertificatesAlone(t *testing.T) {
	dir := certDir(t)

	first, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatalf("read ca: %v", err)
	}

	if err := servicetls.Generate(dir); err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}

	second, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatalf("read ca: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("the second run replaced the CA, invalidating every certificate it had signed")
	}
}

// A client that trusts the first CA must still be able to call a server that
// started after the generator ran again. This is the failure as it was actually
// seen, rather than a comparison of file contents.
func TestAClientKeepsWorkingAcrossASecondGenerate(t *testing.T) {
	dir := certDir(t)
	fix, ctx := startStockServer(t, dir)

	client, err := grpcstock.Dial(fix.addr, dir, "localhost")
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// Stands in for the init container running again on the next compose up.
	if err := servicetls.Generate(dir); err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}

	if _, err := client.Reserve(ctx, "mtls-regen", oneLine(fix.item.ID, 1)); err != nil {
		t.Fatalf("Reserve() after regeneration error = %v", err)
	}
}

// An incomplete set is replaced rather than trusted: half a set is not a set.
func TestAMissingFileCausesAFullRegeneration(t *testing.T) {
	dir := certDir(t)

	if err := os.Remove(filepath.Join(dir, "client-key.pem")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := servicetls.Generate(dir); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	for _, name := range []string{"ca.pem", "server.pem", "server-key.pem", "client.pem", "client-key.pem"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing after regeneration: %v", name, err)
		}
	}
}
