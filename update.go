package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

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

func compareSemver(a, b string) int {
	// Non-release builds never trigger updates
	if a == "dev" || b == "dev" {
		return 0
	}
	aParts := strings.Split(strings.TrimPrefix(a, "v"), ".")
	bParts := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < 3; i++ {
		var ai, bi int
		if i < len(aParts) {
			ai, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bi, _ = strconv.Atoi(bParts[i])
		}
		if ai != bi {
			return ai - bi
		}
	}
	return 0
}

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

	if cachedUpdate != nil && time.Since(cachedUpdateTime) < time.Duration(UpdateCacheTTLHours)*time.Hour {
		return cachedUpdate, nil
	}

	client := &http.Client{Timeout: time.Duration(UpdateCheckTimeout) * time.Second}
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

	suffix := getAssetSuffix()

	info := &UpdateInfo{
		Available:      compareSemver(release.TagName, Version) > 0,
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
	trusted := false
	for _, prefix := range TrustedDownloadPrefixes {
		if strings.HasPrefix(downloadURL, prefix) {
			trusted = true
			break
		}
	}
	if !trusted {
		return fmt.Errorf("untrusted download URL: %s", downloadURL)
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("cannot resolve executable path: %w", err)
	}

	log.Printf("[update] Downloading from %s", downloadURL)
	client := &http.Client{Timeout: time.Duration(UpdateDownloadTimeout) * time.Minute}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	tmpPath := exePath + ".update"
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("cannot create temp file: %w", err)
	}

	if _, err := io.Copy(tmpFile, io.LimitReader(resp.Body, MaxDownloadSize)); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("download write failed: %w", err)
	}
	tmpFile.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod failed: %w", err)
	}

	backupPath := exePath + ".backup"
	os.Remove(backupPath)
	if err := os.Rename(exePath, backupPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("backup failed: %w", err)
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		os.Rename(backupPath, exePath)
		return fmt.Errorf("replace failed: %w", err)
	}
	os.Remove(backupPath)

	log.Printf("[update] Binary replaced successfully. Restarting...")

	execErr := syscallExec(exePath)
	if execErr != nil {
		log.Printf("[update] Auto-restart failed: %v — please restart manually", execErr)
	}
	return nil
}

// syscallExec replaces the current process with a new one.
// Unix: uses syscall.Exec to replace in-place (PID preserved for service managers).
// Windows: starts a new process and exits (service manager restarts).
func syscallExec(path string) error {
	if runtime.GOOS == "windows" {
		vbsPath := path + ".restart.vbs"
		vbs := fmt.Sprintf("WScript.Sleep 500\r\nCreateObject(\"Wscript.Shell\").Run \"\"\"%s\"\"\", 0, False\r\n", path)
		if err := os.WriteFile(vbsPath, []byte(vbs), 0644); err != nil {
			cmd := exec.Command(path)
			if err := cmd.Start(); err != nil {
				return fmt.Errorf("failed to start new process: %w", err)
			}
			os.Exit(0)
			return nil
		}
		exec.Command("wscript.exe", vbsPath).Start()
		os.Exit(0)
		return nil
	}
	return syscall.Exec(path, []string{path}, os.Environ())
}

// ─── Update Handlers ───────────────────────────────────────────────────────

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

func handleUpdateApply(w http.ResponseWriter, _ *http.Request) {
	info, err := checkForUpdate()
	if err != nil || !info.Available || info.DownloadURL == "" {
		writeJSON(w, 400, Response{Success: false, Error: "No update available"})
		return
	}

	writeJSON(w, 200, Response{Success: true, Message: "Updating... NME Print Bridge will restart."})

	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := performUpdate(info.DownloadURL); err != nil {
			log.Printf("[update] Failed: %v", err)
		}
	}()
}
