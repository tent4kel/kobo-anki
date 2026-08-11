package main

import (
	"crypto/tls"
	"crypto/x509"
	"log"
	"net/http"
	"os"
	"time"
)

// newHTTPClient builds an http.Client trusting both the OS's system CA pool
// (if any) and the bundled cacert.pem (see Config.CACertPath) - needed
// because some Kobo firmware ships with no CA bundle at all (verified: no
// /etc/ssl/certs/ca-certificates.crt on a real device), which makes Go's
// default system-pool TLS verification reject every HTTPS certificate,
// valid or not, identically across totally unrelated services (Lexicala,
// PONS, and OpenAI all failed the same "signed by unknown authority"
// way on that device). A missing/unreadable caCertPath degrades to
// whatever the system pool alone provides - fine on a normal dev machine,
// which is why provider tests (plain HTTP, no TLS involved at all) don't
// need to care about this either way.
func newHTTPClient(timeout time.Duration, caCertPath string) *http.Client {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if data, err := os.ReadFile(caCertPath); err != nil {
		log.Printf("No bundled CA file at %s (%v) - relying on the system trust store only", caCertPath, err)
	} else if !pool.AppendCertsFromPEM(data) {
		log.Printf("cacert.pem at %s: no certificates found in it", caCertPath)
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
}
