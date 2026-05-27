package advanced_routing

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"net/http"
	"sync"

	"loadbalancer/internal/backend"
)

type MTLSConfig struct {
	Enabled          bool          `json:"enabled"`
	CACertFile       string        `json:"ca_cert_file"`
	ClientCertFile   string        `json:"client_cert_file"`
	ClientKeyFile    string        `json:"client_key_file"`
	ServerCertFile   string        `json:"server_cert_file"`
	ServerKeyFile    string        `json:"server_key_file"`
	SkipVerify       bool          `json:"skip_verify"`
	ClientAuth       string        `json:"client_auth"` // "none", "request", "require", "verify-if-given", "require-any", "require-and-verify"
	MinVersion       uint16        `json:"min_version"`
	CipherSuites     []uint16      `json:"cipher_suites"`
	CurvePreferences []tls.CurveID `json:"curve_preferences"`
}

type MTLSManager struct {
	config     MTLSConfig
	caCertPool *x509.CertPool
	clientTLS  *tls.Config
	serverTLS  *tls.Config
	mu         sync.RWMutex
}

func NewMTLSManager(config MTLSConfig) (*MTLSManager, error) {
	mm := &MTLSManager{
		config: config,
	}

	if !config.Enabled {
		return mm, nil
	}

	// Load CA certificate
	if config.CACertFile != "" {
		caCert, err := ioutil.ReadFile(config.CACertFile)
		if err != nil {
			return nil, err
		}

		mm.caCertPool = x509.NewCertPool()
		if !mm.caCertPool.AppendCertsFromPEM(caCert) {
			return nil, err
		}
	}

	// Create client TLS configuration
	if config.ClientCertFile != "" && config.ClientKeyFile != "" {
		clientCert, err := tls.LoadX509KeyPair(config.ClientCertFile, config.ClientKeyFile)
		if err != nil {
			return nil, err
		}

		mm.clientTLS = &tls.Config{
			Certificates:       []tls.Certificate{clientCert},
			RootCAs:            mm.caCertPool,
			InsecureSkipVerify: config.SkipVerify,
			MinVersion:         config.MinVersion,
			CipherSuites:       config.CipherSuites,
			CurvePreferences:   config.CurvePreferences,
		}
	}

	// Create server TLS configuration
	if config.ServerCertFile != "" && config.ServerKeyFile != "" {
		serverCert, err := tls.LoadX509KeyPair(config.ServerCertFile, config.ServerKeyFile)
		if err != nil {
			return nil, err
		}

		clientAuth := tls.NoClientCert
		switch config.ClientAuth {
		case "request":
			clientAuth = tls.RequestClientCert
		case "require":
			clientAuth = tls.RequireAnyClientCert
		case "verify-if-given":
			clientAuth = tls.VerifyClientCertIfGiven
		case "require-any":
			clientAuth = tls.RequireAnyClientCert
		case "require-and-verify":
			clientAuth = tls.RequireAndVerifyClientCert
		}

		mm.serverTLS = &tls.Config{
			Certificates:     []tls.Certificate{serverCert},
			ClientCAs:        mm.caCertPool,
			ClientAuth:       clientAuth,
			MinVersion:       config.MinVersion,
			CipherSuites:     config.CipherSuites,
			CurvePreferences: config.CurvePreferences,
		}
	}

	return mm, nil
}

func (mm *MTLSManager) GetClientTLSConfig() *tls.Config {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.clientTLS
}

func (mm *MTLSManager) GetServerTLSConfig() *tls.Config {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.serverTLS
}

func (mm *MTLSManager) WrapHTTPClient(client *http.Client) *http.Client {
	if mm.clientTLS == nil {
		return client
	}

	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	if httpTransport, ok := transport.(*http.Transport); ok {
		httpTransport.TLSClientConfig = mm.clientTLS
	}

	return client
}

func (mm *MTLSManager) VerifyClientCert(req *http.Request) bool {
	if !mm.config.Enabled || mm.config.ClientAuth == "none" {
		return true
	}

	if req.TLS == nil || len(req.TLS.PeerCertificates) == 0 {
		return mm.config.ClientAuth == "none" || mm.config.ClientAuth == "request"
	}

	// Verify client certificate against CA
	if mm.caCertPool != nil {
		opts := x509.VerifyOptions{
			Roots: mm.caCertPool,
		}

		for _, cert := range req.TLS.PeerCertificates {
			if _, err := cert.Verify(opts); err == nil {
				return true
			}
		}
	}

	return false
}

// BackendMTLSManager manages mTLS for individual backends
type BackendMTLSManager struct {
	backends map[string]*MTLSManager
	mu       sync.RWMutex
}

func NewBackendMTLSManager() *BackendMTLSManager {
	return &BackendMTLSManager{
		backends: make(map[string]*MTLSManager),
	}
}

func (bmm *BackendMTLSManager) AddBackend(backend backend.Backend, config MTLSConfig) error {
	mm, err := NewMTLSManager(config)
	if err != nil {
		return err
	}

	bmm.mu.Lock()
	defer bmm.mu.Unlock()
	bmm.backends[backend.GetURL().String()] = mm
	return nil
}

func (bmm *BackendMTLSManager) RemoveBackend(backend backend.Backend) {
	bmm.mu.Lock()
	defer bmm.mu.Unlock()
	delete(bmm.backends, backend.GetURL().String())
}

func (bmm *BackendMTLSManager) GetClientForBackend(backend backend.Backend, client *http.Client) *http.Client {
	bmm.mu.RLock()
	defer bmm.mu.RUnlock()

	if mm, exists := bmm.backends[backend.GetURL().String()]; exists {
		return mm.WrapHTTPClient(client)
	}

	return client
}

func (bmm *BackendMTLSManager) VerifyBackendCert(backend backend.Backend, req *http.Request) bool {
	bmm.mu.RLock()
	defer bmm.mu.RUnlock()

	if mm, exists := bmm.backends[backend.GetURL().String()]; exists {
		return mm.VerifyClientCert(req)
	}

	return true
}

// ZeroTrustRouter implements zero-trust networking with mTLS
type ZeroTrustRouter struct {
	backends     []backend.Backend
	mtlsManager  *BackendMTLSManager
	allowedCerts map[string]bool // Certificate fingerprints that are allowed
	mu           sync.RWMutex
}

func NewZeroTrustRouter(mtlsConfig MTLSConfig) (*ZeroTrustRouter, error) {
	_, err := NewMTLSManager(mtlsConfig)
	if err != nil {
		return nil, err
	}

	return &ZeroTrustRouter{
		backends:     make([]backend.Backend, 0),
		mtlsManager:  NewBackendMTLSManager(),
		allowedCerts: make(map[string]bool),
	}, nil
}

func (ztr *ZeroTrustRouter) SetBackends(backends []backend.Backend, backendMTLSConfigs map[string]MTLSConfig) error {
	ztr.mu.Lock()
	defer ztr.mu.Unlock()

	// Clear existing backends
	ztr.backends = backends

	// Add backends with their mTLS configurations
	for _, backend := range backends {
		backendURL := backend.GetURL().String()
		if config, exists := backendMTLSConfigs[backendURL]; exists {
			if err := ztr.mtlsManager.AddBackend(backend, config); err != nil {
				return err
			}
		}
	}

	return nil
}

func (ztr *ZeroTrustRouter) Route(req *http.Request) backend.Backend {
	ztr.mu.RLock()
	defer ztr.mu.RUnlock()

	// Verify client certificate in zero-trust model
	if !ztr.verifyClientCertificate(req) {
		return nil
	}

	// Select backend
	for _, backend := range ztr.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			return backend
		}
	}

	return nil
}

func (ztr *ZeroTrustRouter) verifyClientCertificate(req *http.Request) bool {
	if req.TLS == nil || len(req.TLS.PeerCertificates) == 0 {
		return false
	}

	// Check if certificate is in allowed list
	for _, cert := range req.TLS.PeerCertificates {
		fingerprint := getCertificateFingerprint(cert)
		if ztr.allowedCerts[fingerprint] {
			return true
		}
	}

	return false
}

func (ztr *ZeroTrustRouter) AddAllowedCertificate(fingerprint string) {
	ztr.mu.Lock()
	defer ztr.mu.Unlock()
	ztr.allowedCerts[fingerprint] = true
}

func (ztr *ZeroTrustRouter) RemoveAllowedCertificate(fingerprint string) {
	ztr.mu.Lock()
	defer ztr.mu.Unlock()
	delete(ztr.allowedCerts, fingerprint)
}

func (ztr *ZeroTrustRouter) GetClientForBackend(backend backend.Backend, client *http.Client) *http.Client {
	return ztr.mtlsManager.GetClientForBackend(backend, client)
}

func getCertificateFingerprint(cert *x509.Certificate) string {
	// Simple fingerprint implementation
	// In practice, you'd use SHA-256 or similar
	return string(cert.Raw)
}

// ServiceMeshIntegration for service-to-service mTLS
type ServiceMeshIntegration struct {
	mtlsManager    *MTLSManager
	serviceName    string
	serviceAccount string
	namespace      string
}

func NewServiceMeshIntegration(config MTLSConfig, serviceName, serviceAccount, namespace string) (*ServiceMeshIntegration, error) {
	mm, err := NewMTLSManager(config)
	if err != nil {
		return nil, err
	}

	return &ServiceMeshIntegration{
		mtlsManager:    mm,
		serviceName:    serviceName,
		serviceAccount: serviceAccount,
		namespace:      namespace,
	}, nil
}

func (smi *ServiceMeshIntegration) GetServiceIdentity() string {
	return smi.namespace + "/" + smi.serviceAccount + "/" + smi.serviceName
}

func (smi *ServiceMeshIntegration) CreateAuthenticatedClient(client *http.Client) *http.Client {
	return smi.mtlsManager.WrapHTTPClient(client)
}

func (smi *ServiceMeshIntegration) VerifyServiceIdentity(req *http.Request, expectedService string) bool {
	if req.TLS == nil || len(req.TLS.PeerCertificates) == 0 {
		return false
	}

	// Verify certificate contains expected service identity
	for _, cert := range req.TLS.PeerCertificates {
		if cert.Subject.CommonName == expectedService {
			return true
		}

		// Check SAN (Subject Alternative Names)
		for _, name := range cert.DNSNames {
			if name == expectedService {
				return true
			}
		}
	}

	return false
}
