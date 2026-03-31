package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ─── Certificate Types ─────────────────────────────────────────────────────

type CertPayload struct {
	HotelID        string   `json:"hotel_id"`
	HotelName      string   `json:"hotel_name"`
	AllowedOrigins []string `json:"allowed_origins"`
	IssuedAt       string   `json:"issued_at"`
	ExpiresAt      string   `json:"expires_at"`
}

type SignedCert struct {
	Payload   CertPayload `json:"payload"`
	Signature string      `json:"signature"` // base64 ed25519 signature of JSON payload
}

// ─── Certificate Manager ───────────────────────────────────────────────────

type CertManager struct {
	mu             sync.RWMutex
	cert           *SignedCert
	allowedOrigins map[string]bool
	rootPubKey     ed25519.PublicKey
	config         Config
	cachePath      string
}

func NewCertManager(cfg Config) (*CertManager, error) {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(RootPublicKeyB64)
	if err != nil {
		return nil, fmt.Errorf("invalid root public key: %w", err)
	}

	cm := &CertManager{
		rootPubKey:     ed25519.PublicKey(pubKeyBytes),
		config:         cfg,
		allowedOrigins: make(map[string]bool),
		cachePath:      filepath.Join(configDir(), CertCacheFile),
	}

	// Allow localhost origins in dev mode only
	if isDevMode() {
		for _, origin := range LocalhostOrigins {
			cm.allowedOrigins[origin] = true
		}
	}

	// Built-in production origins (hardcoded for this version)
	for _, origin := range BuiltInAllowedOrigins {
		cm.allowedOrigins[origin] = true
	}

	return cm, nil
}

func (cm *CertManager) IsOriginAllowed(origin string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.allowedOrigins[origin]
}

func (cm *CertManager) FetchAndVerify() error {
	if cm.config.HotelID == "" {
		return fmt.Errorf("no hotel_id configured")
	}

	url := fmt.Sprintf("%s/%s", cm.config.CertURL, cm.config.HotelID)
	log.Printf("[cert] Fetching certificate from %s", url)

	client := &http.Client{Timeout: time.Duration(CertFetchTimeout) * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return cm.loadCachedCert()
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("[cert] Server returned %d, trying cache", resp.StatusCode)
		return cm.loadCachedCert()
	}

	var apiResp struct {
		Success bool       `json:"success"`
		Data    SignedCert `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return cm.loadCachedCert()
	}

	if !apiResp.Success {
		return cm.loadCachedCert()
	}

	cert := &apiResp.Data
	if err := cm.verifyCert(cert); err != nil {
		return fmt.Errorf("certificate verification failed: %w", err)
	}

	cm.applyCert(cert)
	cm.cacheCert(cert)
	log.Printf("[cert] Certificate verified for %s (%s)", cert.Payload.HotelName, cert.Payload.HotelID)
	return nil
}

func (cm *CertManager) verifyCert(cert *SignedCert) error {
	payloadBytes, err := json.Marshal(cert.Payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	sigBytes, err := base64.StdEncoding.DecodeString(cert.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}

	if !ed25519.Verify(cm.rootPubKey, payloadBytes, sigBytes) {
		return fmt.Errorf("signature verification failed")
	}

	// Check not-before
	if cert.Payload.IssuedAt != "" {
		issuedAt, err := time.Parse(time.RFC3339, cert.Payload.IssuedAt)
		if err != nil {
			return fmt.Errorf("invalid issued_at: %w", err)
		}
		if time.Now().Before(issuedAt) {
			return fmt.Errorf("certificate not yet valid (issued_at: %s)", cert.Payload.IssuedAt)
		}
	}

	// Check expiry
	expiresAt, err := time.Parse(time.RFC3339, cert.Payload.ExpiresAt)
	if err != nil {
		return fmt.Errorf("invalid expires_at: %w", err)
	}
	if time.Now().After(expiresAt) {
		return fmt.Errorf("certificate expired at %s", cert.Payload.ExpiresAt)
	}

	return nil
}

func (cm *CertManager) applyCert(cert *SignedCert) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.cert = cert
	cm.allowedOrigins = make(map[string]bool)
	if isDevMode() {
		for _, origin := range LocalhostOrigins {
			cm.allowedOrigins[origin] = true
		}
	}
	for _, origin := range BuiltInAllowedOrigins {
		cm.allowedOrigins[origin] = true
	}
	for _, origin := range cert.Payload.AllowedOrigins {
		cm.allowedOrigins[origin] = true
	}
}

func (cm *CertManager) cacheCert(cert *SignedCert) {
	os.MkdirAll(configDir(), 0755)
	data, _ := json.MarshalIndent(cert, "", "  ")
	os.WriteFile(cm.cachePath, data, 0600)
}

func (cm *CertManager) loadCachedCert() error {
	data, err := os.ReadFile(cm.cachePath)
	if err != nil {
		return fmt.Errorf("no cached certificate available")
	}

	var cert SignedCert
	if err := json.Unmarshal(data, &cert); err != nil {
		return fmt.Errorf("invalid cached certificate")
	}

	if err := cm.verifyCert(&cert); err != nil {
		return fmt.Errorf("cached certificate invalid: %w", err)
	}

	cm.applyCert(&cert)
	log.Printf("[cert] Using cached certificate for %s", cert.Payload.HotelName)
	return nil
}

func (cm *CertManager) StartPeriodicRefresh() {
	go func() {
		ticker := time.NewTicker(time.Duration(CertRefreshHours) * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := cm.FetchAndVerify(); err != nil {
				log.Printf("[cert] Periodic refresh failed: %v", err)
			}
		}
	}()
}
