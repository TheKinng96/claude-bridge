package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// StartTLS starts an HTTPS server on tlsPort that serves the same routes as
// the main HTTP server. It auto-generates a self-signed certificate for
// 127.0.0.1 and localhost, stored in dataDir so it persists across restarts.
func (s *Server) StartTLS(tlsPort int, dataDir string) error {
	certPath := filepath.Join(dataDir, "cert.pem")
	keyPath := filepath.Join(dataDir, "key.pem")

	// Generate cert if it doesn't exist or is expired.
	if err := ensureCert(certPath, keyPath); err != nil {
		return fmt.Errorf("ensure TLS cert: %w", err)
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("load TLS cert: %w", err)
	}

	// Build the same mux as the HTTP server.
	mux := s.buildMux()

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	addr := fmt.Sprintf("127.0.0.1:%d", tlsPort)
	ln, err := tls.Listen("tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("TLS listen on %s: %w", addr, err)
	}

	s.mu.Lock()
	s.tlsListener = ln
	s.mu.Unlock()

	go func() {
		log.Printf("CRM Agent HTTPS server running at https://%s", addr)
		log.Printf("  → Claude Desktop connector URL: https://127.0.0.1:%d/mcp/sse", tlsPort)
		if err := http.Serve(ln, mux); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTPS server error: %v", err)
		}
	}()

	return nil
}

// ensureCert generates a self-signed certificate if one doesn't exist or is
// expiring within 30 days.
func ensureCert(certPath, keyPath string) error {
	// Check if cert exists and is still valid.
	if _, err := os.Stat(certPath); err == nil {
		certPEM, err := os.ReadFile(certPath)
		if err == nil {
			block, _ := pem.Decode(certPEM)
			if block != nil {
				cert, err := x509.ParseCertificate(block.Bytes)
				if err == nil && time.Until(cert.NotAfter) > 30*24*time.Hour {
					return nil // cert is still valid
				}
			}
		}
	}

	log.Println("[tls] Generating self-signed certificate for localhost...")

	// Generate ECDSA key.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"CRM Agent (local)"},
			CommonName:   "127.0.0.1",
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour), // 1 year

		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,

		IPAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
			net.ParseIP("::1"),
		},
		DNSNames: []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	// Ensure directory exists.
	os.MkdirAll(filepath.Dir(certPath), 0700)

	// Write cert PEM.
	certFile, err := os.Create(certPath)
	if err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certFile.Close()

	// Write key PEM.
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyFile, err := os.Create(keyPath)
	if err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	keyFile.Close()

	log.Printf("[tls] Certificate generated: %s", certPath)
	return nil
}
