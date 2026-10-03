package forward

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CA is per-run unless explicitly loaded from administrator-provided files.
type CA struct {
	Certificate tls.Certificate
	PEM         []byte
}

func NewCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Circuit Local Inspection CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}
func LoadCA(certPath, keyPath string) (*CA, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	if !cert.IsCA || time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
		return nil, fmt.Errorf("CA must be a currently valid CA certificate")
	}
	pair.Leaf = cert
	return &CA{Certificate: pair, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})}, nil
}

// WriteBundle includes system roots where available plus this run's CA.
func (ca *CA) WriteBundle(dir string) (string, error) {
	var bundle []byte
	if custom := os.Getenv("SSL_CERT_FILE"); custom != "" {
		data, err := os.ReadFile(custom)
		if err != nil {
			return "", err
		}
		bundle = append(bundle, data...)
	} else {
		for _, path := range []string{"/etc/ssl/cert.pem", "/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt"} {
			if data, err := os.ReadFile(path); err == nil {
				bundle = append(bundle, data...)
				break
			}
		}
	}
	bundle = append(bundle, '\n')
	bundle = append(bundle, ca.PEM...)
	path := filepath.Join(dir, "ca-bundle.pem")
	return path, os.WriteFile(path, bundle, 0600)
}
