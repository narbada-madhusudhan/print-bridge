// NME Print Bridge — Thermal Printer Service for Hotels
// Single binary, zero dependencies. Download and run.
//
// Connects web-based POS systems to thermal printers via HTTP.
// Uses certificate-based trust for multi-hotel security.
//
// Endpoints:
//   GET  /             Status + info
//   GET  /health       Health check
//   GET  /printers     List installed printers
//   POST /print/network  Print to network printer (TCP)
//   POST /print/usb      Print to USB/OS printer
//   POST /test           Test printer connectivity

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Version is set at build time via: go build -ldflags "-X main.Version=vX.Y.Z"
var Version = "dev"

const Port = 9120

// Root public key — baked in at compile time via ldflags
// Override with: go build -ldflags "-X main.RootPublicKeyB64=..."
var RootPublicKeyB64 = "PYpHIvPZS5ynAaz2iUy0iD3FAiizQ1Wi0Ee7AUHb2Ho="

// Default cert URL — override via config or CLI flag
var DefaultCertURL = "https://printbridge.narbadatech.com/api/certs"

// Built-in allowed origins — always allowed regardless of certificate.
// These are baked in for production use before the central cert API is live.
// Remove once cert system is fully deployed.
var BuiltInAllowedOrigins = []string{
	"https://godawariresort.com",
	"http://godawariresort.com",
	"https://admin.godawariresort.com",
	"https://pos.godawariresort.com",
	"https://www.godawariresort.com",
}

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

// ─── Config ────────────────────────────────────────────────────────────────

type Config struct {
	HotelID string `json:"hotel_id"`
	CertURL string `json:"cert_url"`
}

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".printbridge")
}

func configPath() string {
	return filepath.Join(configDir(), "config.json")
}

func loadConfig() Config {
	cfg := Config{CertURL: DefaultCertURL}
	data, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	json.Unmarshal(data, &cfg)
	if cfg.CertURL == "" {
		cfg.CertURL = DefaultCertURL
	}
	return cfg
}

func saveConfig(cfg Config) {
	os.MkdirAll(configDir(), 0755)
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(configPath(), data, 0644)
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
		cachePath:      filepath.Join(configDir(), "cert-cache.json"),
	}

	// Always allow localhost for development
	cm.allowedOrigins["http://localhost:3000"] = true
	cm.allowedOrigins["http://localhost:3001"] = true
	cm.allowedOrigins["https://localhost:3000"] = true

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

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		// Try loading cached cert
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
	// Serialize payload to JSON for signature verification
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
	// Reset and rebuild allowed origins
	cm.allowedOrigins = map[string]bool{
		"http://localhost:3000":  true,
		"http://localhost:3001":  true,
		"https://localhost:3000": true,
	}
	// Built-in origins always allowed
	for _, origin := range BuiltInAllowedOrigins {
		cm.allowedOrigins[origin] = true
	}
	// Certificate origins
	for _, origin := range cert.Payload.AllowedOrigins {
		cm.allowedOrigins[origin] = true
	}
}

func (cm *CertManager) cacheCert(cert *SignedCert) {
	os.MkdirAll(configDir(), 0755)
	data, _ := json.MarshalIndent(cert, "", "  ")
	os.WriteFile(cm.cachePath, data, 0644)
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
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := cm.FetchAndVerify(); err != nil {
				log.Printf("[cert] Periodic refresh failed: %v", err)
			}
		}
	}()
}

// ─── HTTP Types ────────────────────────────────────────────────────────────

type Response struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

type PrinterInfo struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type NetworkPrintReq struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	Data string `json:"data"` // base64
	Raw  string `json:"raw"`  // plain text
}

type USBPrintReq struct {
	Printer string `json:"printer"`
	Data    string `json:"data"`
	Raw     string `json:"raw"`
}

type TestReq struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// ─── CORS Middleware ───────────────────────────────────────────────────────

func corsMiddleware(cm *CertManager, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if cm.IsOriginAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ─── Handlers ──────────────────────────────────────────────────────────────

func handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"name":     "NME Print Bridge",
		"version":  Version,
		"platform": runtime.GOOS,
		"arch":     runtime.GOARCH,
		"status":   "running",
	})
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, Response{Success: true, Message: "ok"})
}

func handleListPrinters(w http.ResponseWriter, _ *http.Request) {
	printers, err := listPrinters()
	if err != nil {
		writeJSON(w, 500, Response{Success: false, Error: err.Error()})
		return
	}
	writeJSON(w, 200, Response{Success: true, Data: printers})
}

func handlePrintNetwork(w http.ResponseWriter, r *http.Request) {
	var req NetworkPrintReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, Response{Success: false, Error: "Invalid JSON"})
		return
	}
	if req.IP == "" {
		writeJSON(w, 400, Response{Success: false, Error: "ip is required"})
		return
	}
	if req.Port == 0 {
		req.Port = 9100
	}

	printData := decodeData(req.Data, req.Raw)
	if len(printData) == 0 {
		writeJSON(w, 400, Response{Success: false, Error: "No print data"})
		return
	}

	addr := fmt.Sprintf("%s:%d", req.IP, req.Port)
	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		writeJSON(w, 500, Response{Success: false, Error: fmt.Sprintf("Connection failed: %s", err.Error())})
		return
	}
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	if _, err = conn.Write(printData); err != nil {
		writeJSON(w, 500, Response{Success: false, Error: fmt.Sprintf("Write failed: %s", err.Error())})
		return
	}

	writeJSON(w, 200, Response{Success: true, Message: fmt.Sprintf("Sent %d bytes to %s", len(printData), addr)})
}

func handlePrintUSB(w http.ResponseWriter, r *http.Request) {
	var req USBPrintReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, Response{Success: false, Error: "Invalid JSON"})
		return
	}
	if req.Printer == "" {
		writeJSON(w, 400, Response{Success: false, Error: "printer name is required"})
		return
	}

	printData := decodeData(req.Data, req.Raw)
	if len(printData) == 0 {
		writeJSON(w, 400, Response{Success: false, Error: "No print data"})
		return
	}

	if err := printToUSB(req.Printer, printData); err != nil {
		writeJSON(w, 500, Response{Success: false, Error: err.Error()})
		return
	}

	writeJSON(w, 200, Response{Success: true, Message: fmt.Sprintf("Sent to %s", req.Printer)})
}

func handleTest(w http.ResponseWriter, r *http.Request) {
	var req TestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, Response{Success: false, Error: "Invalid JSON"})
		return
	}
	if req.IP == "" {
		writeJSON(w, 400, Response{Success: false, Error: "ip is required"})
		return
	}
	if req.Port == 0 {
		req.Port = 9100
	}

	addr := fmt.Sprintf("%s:%d", req.IP, req.Port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		writeJSON(w, 200, map[string]any{"success": true, "online": false, "error": err.Error()})
		return
	}
	conn.Close()
	writeJSON(w, 200, map[string]any{"success": true, "online": true})
}

// ─── Helpers ───────────────────────────────────────────────────────────────

func decodeData(b64, raw string) []byte {
	if b64 != "" {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err == nil {
			return data
		}
	}
	return []byte(raw)
}

func listPrinters() ([]PrinterInfo, error) {
	switch runtime.GOOS {
	case "darwin", "linux":
		return listPrintersUnix()
	case "windows":
		return listPrintersWindows()
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func listPrintersUnix() ([]PrinterInfo, error) {
	out, err := exec.Command("lpstat", "-p").CombinedOutput()
	if err != nil {
		return []PrinterInfo{}, nil
	}
	var printers []PrinterInfo
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "printer ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				printers = append(printers, PrinterInfo{
					Name:    parts[1],
					Enabled: strings.Contains(line, "enabled"),
				})
			}
		}
	}
	return printers, nil
}

func listPrintersWindows() ([]PrinterInfo, error) {
	out, err := exec.Command("powershell", "-Command",
		`Get-Printer | Select-Object Name, PrinterStatus | ConvertTo-Json`).CombinedOutput()
	if err != nil {
		return []PrinterInfo{}, nil
	}
	var raw any
	if err := json.Unmarshal(out, &raw); err != nil {
		return []PrinterInfo{}, nil
	}
	var items []map[string]any
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				items = append(items, m)
			}
		}
	case map[string]any:
		items = append(items, v)
	}
	var printers []PrinterInfo
	for _, item := range items {
		name, _ := item["Name"].(string)
		printers = append(printers, PrinterInfo{Name: name, Enabled: true})
	}
	return printers, nil
}

func printToUSB(printerName string, data []byte) error {
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("print-%d.raw", time.Now().UnixNano()))
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	defer os.Remove(tmpFile)

	switch runtime.GOOS {
	case "darwin", "linux":
		cmd := exec.Command("lp", "-d", printerName, "-o", "raw", tmpFile)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("lp error: %s — %s", err, string(out))
		}
	case "windows":
		cmd := exec.Command("cmd", "/c", fmt.Sprintf(`copy /b "%s" "\\localhost\%s"`, tmpFile, printerName))
		if err := cmd.Run(); err != nil {
			ps := exec.Command("powershell", "-Command",
				fmt.Sprintf(`Get-Content '%s' -Encoding Byte -ReadCount 0 | Out-Printer '%s'`, tmpFile, printerName))
			if out, err := ps.CombinedOutput(); err != nil {
				return fmt.Errorf("print error: %s — %s", err, string(out))
			}
		}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return nil
}

// ─── Self-Update ───────────────────────────────────────────────────────────

const GitHubRepo = "narbada-madhusudhan/nme-print-bridge"

type UpdateInfo struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	DownloadURL    string `json:"download_url,omitempty"`
	ReleaseURL     string `json:"release_url,omitempty"`
}

var (
	cachedUpdate     *UpdateInfo
	cachedUpdateTime time.Time
	updateMu         sync.Mutex
)

func getAssetSuffix() string {
	switch {
	case runtime.GOOS == "windows":
		return "windows-amd64.exe"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "mac-arm64"
	case runtime.GOOS == "darwin":
		return "mac-amd64"
	default:
		return "linux-amd64"
	}
}

func checkForUpdate() (*UpdateInfo, error) {
	updateMu.Lock()
	defer updateMu.Unlock()

	// Cache for 1 hour
	if cachedUpdate != nil && time.Since(cachedUpdateTime) < time.Hour {
		return cachedUpdate, nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", GitHubRepo))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(Version, "v")
	suffix := getAssetSuffix()

	info := &UpdateInfo{
		Available:      latest != current && latest > current,
		CurrentVersion: Version,
		LatestVersion:  release.TagName,
		ReleaseURL:     release.HTMLURL,
	}

	for _, asset := range release.Assets {
		if strings.HasSuffix(asset.Name, suffix) {
			info.DownloadURL = asset.BrowserDownloadURL
			break
		}
	}

	cachedUpdate = info
	cachedUpdateTime = time.Now()
	return info, nil
}

func performUpdate(downloadURL string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("cannot resolve executable path: %w", err)
	}

	log.Printf("[update] Downloading from %s", downloadURL)
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	// Write to temp file next to current binary
	tmpPath := exePath + ".update"
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("cannot create temp file: %w", err)
	}

	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := tmpFile.Write(buf[:n]); writeErr != nil {
				tmpFile.Close()
				os.Remove(tmpPath)
				return fmt.Errorf("write failed: %w", writeErr)
			}
		}
		if readErr != nil {
			break
		}
	}
	tmpFile.Close()

	// Make executable
	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod failed: %w", err)
	}

	// Replace old binary
	backupPath := exePath + ".backup"
	os.Remove(backupPath) // clean old backup
	if err := os.Rename(exePath, backupPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("backup failed: %w", err)
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		// Restore backup
		os.Rename(backupPath, exePath)
		return fmt.Errorf("replace failed: %w", err)
	}
	os.Remove(backupPath)

	log.Printf("[update] Updated successfully. Restart to use new version.")

	// Restart: exec the new binary (replaces current process)
	execErr := syscallExec(exePath)
	if execErr != nil {
		log.Printf("[update] Auto-restart failed: %v — please restart manually", execErr)
	}
	return nil
}

// syscallExec replaces the current process with a new one (Unix only)
// On Windows, we just exit and let the auto-start mechanism restart us
func syscallExec(path string) error {
	if runtime.GOOS == "windows" {
		// Windows: start new process and exit
		cmd := exec.Command(path)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Start()
		os.Exit(0)
		return nil
	}
	// Unix: replace current process
	return fmt.Errorf("please restart print-bridge to use the new version")
}

// GET /update/check — check for updates
func handleUpdateCheck(w http.ResponseWriter, _ *http.Request) {
	info, err := checkForUpdate()
	if err != nil {
		writeJSON(w, 200, Response{Success: true, Data: &UpdateInfo{
			Available:      false,
			CurrentVersion: Version,
			LatestVersion:  Version,
		}})
		return
	}
	writeJSON(w, 200, Response{Success: true, Data: info})
}

// POST /update/apply — download and apply update
func handleUpdateApply(w http.ResponseWriter, _ *http.Request) {
	info, err := checkForUpdate()
	if err != nil || !info.Available || info.DownloadURL == "" {
		writeJSON(w, 400, Response{Success: false, Error: "No update available"})
		return
	}

	writeJSON(w, 200, Response{Success: true, Message: "Updating... NME Print Bridge will restart."})

	// Apply update in background (response already sent)
	go func() {
		time.Sleep(500 * time.Millisecond) // let response flush
		if err := performUpdate(info.DownloadURL); err != nil {
			log.Printf("[update] Failed: %v", err)
		}
	}()
}

// ─── Auto-Start Install/Uninstall ──────────────────────────────────────────

func installAutoStart() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine executable path: %w", err)
	}
	// Resolve symlinks
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("cannot resolve executable path: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return installMacLaunchAgent(exePath)
	case "windows":
		return installWindowsStartup(exePath)
	case "linux":
		return installLinuxSystemd(exePath)
	default:
		return fmt.Errorf("auto-start not supported on %s — run the binary manually", runtime.GOOS)
	}
}

func uninstallAutoStart() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallMacLaunchAgent()
	case "windows":
		return uninstallWindowsStartup()
	case "linux":
		return uninstallLinuxSystemd()
	default:
		return fmt.Errorf("auto-start not supported on %s", runtime.GOOS)
	}
}

func installMacLaunchAgent(exePath string) error {
	home, _ := os.UserHomeDir()
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	plistPath := filepath.Join(plistDir, "com.nme.print-bridge.plist")

	os.MkdirAll(plistDir, 0755)

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.nme.print-bridge</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/print-bridge.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/print-bridge.log</string>
</dict>
</plist>`, exePath)

	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return err
	}

	exec.Command("launchctl", "unload", plistPath).Run()
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("launchctl load failed: %w", err)
	}

	fmt.Printf("  ✓ Auto-start installed (macOS LaunchAgent)\n")
	fmt.Printf("  ✓ NME Print Bridge will start automatically on login\n")
	fmt.Printf("  ✓ Logs: /tmp/print-bridge.log\n")
	fmt.Printf("  ✓ Uninstall: %s --uninstall\n", exePath)
	return nil
}

func uninstallMacLaunchAgent() error {
	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.nme.print-bridge.plist")
	exec.Command("launchctl", "unload", plistPath).Run()
	os.Remove(plistPath)
	fmt.Println("  ✓ Auto-start removed")
	return nil
}

func installWindowsStartup(exePath string) error {
	home, _ := os.UserHomeDir()
	startupDir := filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup")

	// Create a .bat file in Startup folder that runs the binary minimized
	batPath := filepath.Join(startupDir, "NME Print Bridge.bat")
	bat := fmt.Sprintf("@echo off\r\nstart /min \"\" \"%s\"\r\n", exePath)

	if err := os.WriteFile(batPath, []byte(bat), 0644); err != nil {
		return fmt.Errorf("failed to create startup script: %w", err)
	}

	fmt.Printf("  ✓ Auto-start installed (Windows Startup folder)\n")
	fmt.Printf("  ✓ NME Print Bridge will start automatically on login\n")
	fmt.Printf("  ✓ Uninstall: %s --uninstall\n", exePath)
	return nil
}

func uninstallWindowsStartup() error {
	home, _ := os.UserHomeDir()
	batPath := filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "NME Print Bridge.bat")
	os.Remove(batPath)
	fmt.Println("  ✓ Auto-start removed")
	return nil
}

func installLinuxSystemd(exePath string) error {
	home, _ := os.UserHomeDir()
	serviceDir := filepath.Join(home, ".config", "systemd", "user")
	servicePath := filepath.Join(serviceDir, "print-bridge.service")

	os.MkdirAll(serviceDir, 0755)

	service := fmt.Sprintf(`[Unit]
Description=NME Print Bridge — Thermal Printer Service
After=network.target

[Service]
ExecStart=%s
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, exePath)

	if err := os.WriteFile(servicePath, []byte(service), 0644); err != nil {
		return err
	}

	exec.Command("systemctl", "--user", "daemon-reload").Run()
	exec.Command("systemctl", "--user", "enable", "print-bridge").Run()
	if err := exec.Command("systemctl", "--user", "start", "print-bridge").Run(); err != nil {
		return fmt.Errorf("systemctl start failed: %w", err)
	}

	fmt.Printf("  ✓ Auto-start installed (systemd user service)\n")
	fmt.Printf("  ✓ NME Print Bridge will start automatically on login\n")
	fmt.Printf("  ✓ Uninstall: %s --uninstall\n", exePath)
	return nil
}

func uninstallLinuxSystemd() error {
	exec.Command("systemctl", "--user", "stop", "print-bridge").Run()
	exec.Command("systemctl", "--user", "disable", "print-bridge").Run()
	home, _ := os.UserHomeDir()
	os.Remove(filepath.Join(home, ".config", "systemd", "user", "print-bridge.service"))
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Println("  ✓ Auto-start removed")
	return nil
}

// ─── Main ──────────────────────────────────────────────────────────────────

func main() {
	hotelID := flag.String("hotel-id", "", "Hotel ID for certificate lookup")
	certURL := flag.String("cert-url", "", "Certificate API URL")
	install := flag.Bool("install", false, "Install auto-start (runs on login)")
	uninstall := flag.Bool("uninstall", false, "Remove auto-start")
	flag.Parse()

	// Handle install/uninstall
	if *install {
		if err := installAutoStart(); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ Install failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if *uninstall {
		if err := uninstallAutoStart(); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ Uninstall failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Load or create config
	cfg := loadConfig()
	if *hotelID != "" {
		cfg.HotelID = *hotelID
	}
	if *certURL != "" {
		cfg.CertURL = *certURL
	}
	saveConfig(cfg)

	// Initialize cert manager
	cm, err := NewCertManager(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize: %v", err)
	}

	// Fetch and verify certificate
	if cfg.HotelID != "" {
		if err := cm.FetchAndVerify(); err != nil {
			log.Printf("[cert] Warning: %v (only localhost connections allowed)", err)
		}
		cm.StartPeriodicRefresh()
	} else {
		log.Println("[cert] No hotel_id configured — only localhost connections allowed")
		log.Printf("[cert] Configure: %s --hotel-id YOUR_HOTEL_ID", os.Args[0])
	}

	// Routes
	mux := http.NewServeMux()
	mux.HandleFunc("/", corsMiddleware(cm, handleStatus))
	mux.HandleFunc("/health", corsMiddleware(cm, handleHealth))
	mux.HandleFunc("/printers", corsMiddleware(cm, handleListPrinters))
	mux.HandleFunc("/print/network", corsMiddleware(cm, handlePrintNetwork))
	mux.HandleFunc("/print/usb", corsMiddleware(cm, handlePrintUSB))
	mux.HandleFunc("/test", corsMiddleware(cm, handleTest))
	mux.HandleFunc("/update/check", corsMiddleware(cm, handleUpdateCheck))
	mux.HandleFunc("/update/apply", corsMiddleware(cm, handleUpdateApply))

	addr := fmt.Sprintf("127.0.0.1:%d", Port)

	fmt.Println()
	fmt.Println("  ╔═══════════════════════════════════════╗")
	fmt.Printf("  ║   NME Print Bridge v%-22s║\n", Version)
	fmt.Printf("  ║   http://%-28s║\n", addr)
	fmt.Println("  ╠═══════════════════════════════════════╣")
	fmt.Println("  ║  GET  /              Status            ║")
	fmt.Println("  ║  GET  /printers      List printers     ║")
	fmt.Println("  ║  POST /print/network Network printer   ║")
	fmt.Println("  ║  POST /print/usb     USB printer       ║")
	fmt.Println("  ║  POST /test          Test connection    ║")
	fmt.Println("  ║  GET  /update/check  Check for updates ║")
	fmt.Println("  ║  POST /update/apply  Apply update      ║")
	fmt.Println("  ╚═══════════════════════════════════════╝")
	if cfg.HotelID != "" {
		fmt.Printf("  Hotel: %s\n", cfg.HotelID)
	}

	// Check for updates in background on startup
	go func() {
		info, err := checkForUpdate()
		if err == nil && info.Available {
			fmt.Printf("\n  ⬆ Update available: %s → %s\n", info.CurrentVersion, info.LatestVersion)
			fmt.Printf("  ⬆ Download: %s\n\n", info.ReleaseURL)
		}
	}()

	fmt.Println()
	log.Fatal(http.ListenAndServe(addr, mux))
}
