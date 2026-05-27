package security

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
)

// CertificateManager manages TLS certificates
type CertificateManager struct {
	config CertificateManagerConfig
	logger *zap.Logger

	// Certificate state
	certificate *tls.Certificate
	privateKey  *rsa.PrivateKey
	mu          sync.RWMutex

	// Auto-renewal
	stopRenewal chan struct{}
}

// CertificateManagerConfig contains certificate manager configuration
type CertificateManagerConfig struct {
	AutoRenewal   bool        `json:"auto_renewal"`
	RotationDays  int         `json:"rotation_days"`
	TLSMinVersion string      `json:"tls_min_version"`
	TLSCiphers    []string    `json:"tls_ciphers"`
	Logger        *zap.Logger `json:"-"`
}

// CertificateInfo contains certificate information
type CertificateInfo struct {
	Domain        string    `json:"domain"`
	Issuer        string    `json:"issuer"`
	NotBefore     time.Time `json:"not_before"`
	NotAfter      time.Time `json:"not_after"`
	DaysRemaining int       `json:"days_remaining"`
	AutoRenewed   bool      `json:"auto_renewed"`
}

// NewCertificateManager creates a new certificate manager
func NewCertificateManager(config CertificateManagerConfig) (*CertificateManager, error) {
	cm := &CertificateManager{
		config:      config,
		logger:      config.Logger,
		stopRenewal: make(chan struct{}),
	}

	// Initialize with a self-signed certificate for testing
	if err := cm.generateSelfSignedCertificate("localhost"); err != nil {
		return nil, fmt.Errorf("failed to generate self-signed certificate: %w", err)
	}

	// Start auto-renewal if enabled
	if config.AutoRenewal {
		go cm.startAutoRenewal()
	}

	cm.logger.Info("Certificate manager initialized",
		zap.Bool("auto_renewal", config.AutoRenewal),
		zap.Int("rotation_days", config.RotationDays))

	return cm, nil
}

// GetTLSConfig returns the TLS configuration
func (cm *CertificateManager) GetTLSConfig() (*tls.Config, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if cm.certificate == nil {
		return nil, fmt.Errorf("no certificate available")
	}

	// Create TLS configuration
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*cm.certificate},
		MinVersion:   cm.getTLSVersion(),
		CipherSuites: cm.getTLSCiphers(),
		// Security settings
		PreferServerCipherSuites: true,
		CurvePreferences:         []tls.CurveID{tls.X25519, tls.CurveP256},
	}

	return tlsConfig, nil
}

// generateSelfSignedCertificate generates a self-signed certificate
func (cm *CertificateManager) generateSelfSignedCertificate(domain string) error {
	// Generate private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Load Balancer"},
			CommonName:   domain,
		},
		DNSNames:    []string{domain, "localhost"},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().AddDate(0, 0, cm.config.RotationDays),
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	// Create certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	// Create TLS certificate
	cert := tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  privateKey,
	}

	cm.mu.Lock()
	cm.certificate = &cert
	cm.privateKey = privateKey
	cm.mu.Unlock()

	cm.logger.Info("Self-signed certificate generated",
		zap.String("domain", domain),
		zap.Time("expires", template.NotAfter))

	return nil
}

// getTLSVersion returns the minimum TLS version
func (cm *CertificateManager) getTLSVersion() uint16 {
	switch cm.config.TLSMinVersion {
	case "1.0":
		return tls.VersionTLS10
	case "1.1":
		return tls.VersionTLS11
	case "1.2":
		return tls.VersionTLS12
	case "1.3":
		return tls.VersionTLS13
	default:
		return tls.VersionTLS12 // Default to TLS 1.2
	}
}

// getTLSCiphers returns the cipher suites
func (cm *CertificateManager) getTLSCiphers() []uint16 {
	if len(cm.config.TLSCiphers) == 0 {
		// Use secure default ciphers
		return []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		}
	}

	// Map cipher names to IDs
	cipherMap := map[string]uint16{
		"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384": tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA":    tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
		"TLS_RSA_WITH_AES_256_GCM_SHA384":       tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
		"TLS_RSA_WITH_AES_256_CBC_SHA":          tls.TLS_RSA_WITH_AES_256_CBC_SHA,
	}

	var ciphers []uint16
	for _, cipherName := range cm.config.TLSCiphers {
		if cipherID, exists := cipherMap[cipherName]; exists {
			ciphers = append(ciphers, cipherID)
		}
	}

	return ciphers
}

// startAutoRenewal starts the automatic certificate renewal process
func (cm *CertificateManager) startAutoRenewal() {
	ticker := time.NewTicker(24 * time.Hour) // Check daily
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if cm.shouldRenewCertificate() {
				if err := cm.renewCertificate(); err != nil {
					cm.logger.Error("Failed to renew certificate", zap.Error(err))
				}
			}
		case <-cm.stopRenewal:
			return
		}
	}
}

// shouldRenewCertificate checks if the certificate should be renewed
func (cm *CertificateManager) shouldRenewCertificate() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if cm.certificate == nil {
		return true
	}

	// Parse certificate
	cert, err := x509.ParseCertificate(cm.certificate.Certificate[0])
	if err != nil {
		cm.logger.Error("Failed to parse certificate", zap.Error(err))
		return true
	}

	// Renew if certificate expires within the rotation period
	renewalThreshold := time.Now().AddDate(0, 0, cm.config.RotationDays/2) // Renew at half-life
	return cert.NotAfter.Before(renewalThreshold)
}

// renewCertificate renews the certificate
func (cm *CertificateManager) renewCertificate() error {
	cm.logger.Info("Renewing certificate")

	// Generate new certificate
	if err := cm.generateSelfSignedCertificate("localhost"); err != nil {
		return fmt.Errorf("failed to renew certificate: %w", err)
	}

	cm.logger.Info("Certificate renewed successfully")
	return nil
}

// GetCertificateInfo returns information about the current certificate
func (cm *CertificateManager) GetCertificateInfo() (*CertificateInfo, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if cm.certificate == nil {
		return nil, fmt.Errorf("no certificate available")
	}

	// Parse certificate
	cert, err := x509.ParseCertificate(cm.certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	daysRemaining := int(cert.NotAfter.Sub(time.Now()).Hours() / 24)

	return &CertificateInfo{
		Domain:        cert.Subject.CommonName,
		Issuer:        cert.Issuer.CommonName,
		NotBefore:     cert.NotBefore,
		NotAfter:      cert.NotAfter,
		DaysRemaining: daysRemaining,
		AutoRenewed:   false,
	}, nil
}

// LoadCertificateFromFile loads a certificate from files
func (cm *CertificateManager) LoadCertificateFromFile(certFile, keyFile string) error {
	// Load certificate
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return fmt.Errorf("failed to read certificate file: %w", err)
	}

	// Load private key
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("failed to read private key file: %w", err)
	}

	// Parse certificate and key
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("failed to parse certificate/key pair: %w", err)
	}

	cm.mu.Lock()
	cm.certificate = &cert
	cm.mu.Unlock()

	cm.logger.Info("Certificate loaded from files",
		zap.String("cert_file", certFile),
		zap.String("key_file", keyFile))

	return nil
}

// SaveCertificateToFile saves the current certificate to files
func (cm *CertificateManager) SaveCertificateToFile(certFile, keyFile string) error {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if cm.certificate == nil {
		return fmt.Errorf("no certificate available")
	}

	// Save certificate
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cm.certificate.Certificate[0],
	})

	if err := os.WriteFile(certFile, certPEM, 0644); err != nil {
		return fmt.Errorf("failed to write certificate file: %w", err)
	}

	// Save private key
	keyBytes, err := x509.MarshalPKCS8PrivateKey(cm.privateKey)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyBytes,
	})

	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		return fmt.Errorf("failed to write private key file: %w", err)
	}

	cm.logger.Info("Certificate saved to files",
		zap.String("cert_file", certFile),
		zap.String("key_file", keyFile))

	return nil
}

// Stop stops the certificate manager
func (cm *CertificateManager) Stop() {
	if cm.config.AutoRenewal {
		close(cm.stopRenewal)
	}

	cm.logger.Info("Certificate manager stopped")
}
