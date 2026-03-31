package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ─── Printer Types ─────────────────────────────────────────────────────────

type PrinterInfo struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// ─── Printer Cache ─────────────────────────────────────────────────────────

var (
	printerCache     []PrinterInfo
	printerCacheTime time.Time
	printerCacheMu   sync.Mutex
	printerCacheTTL  = time.Duration(PrinterCacheTTLSeconds) * time.Second
)

func listPrintersCached() ([]PrinterInfo, error) {
	printerCacheMu.Lock()
	defer printerCacheMu.Unlock()
	if printerCache != nil && time.Since(printerCacheTime) < printerCacheTTL {
		return printerCache, nil
	}
	printers, err := listPrinters()
	if err != nil {
		return nil, err
	}
	printerCache = printers
	printerCacheTime = time.Now()
	return printers, nil
}

func validatePrinterName(name string) error {
	printers, err := listPrintersCached()
	if err != nil {
		return fmt.Errorf("cannot list printers: %w", err)
	}
	for _, p := range printers {
		if p.Name == name {
			return nil
		}
	}
	return fmt.Errorf("unknown printer: %s", name)
}

// ─── Printer Listing ───────────────────────────────────────────────────────

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

func probeUSBPrinter(printerName string) bool {
	switch runtime.GOOS {
	case "darwin", "linux":
		statusCmd := []byte{0x10, 0x04, 0x01} // DLE EOT 1 = query printer status
		tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("probe-%d.raw", time.Now().UnixNano()))
		if err := os.WriteFile(tmpFile, statusCmd, 0644); err != nil {
			return false
		}
		defer os.Remove(tmpFile)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(ProbeTimeout)*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "lp", "-d", printerName, "-o", "raw", tmpFile)
		err := cmd.Run()
		if ctx.Err() == context.DeadlineExceeded {
			return false
		}
		return err == nil
	case "windows":
		return canOpenPrinter(printerName)
	}
	return false
}

func listPrintersUnix() ([]PrinterInfo, error) {
	out, err := exec.Command("lpstat", "-p").CombinedOutput()
	if err != nil {
		return []PrinterInfo{}, nil
	}

	// Send DLE EOT probe to USB printers to trigger CUPS offline detection
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "printer ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && !isNetworkPrinter(parts[1]) {
				go probeUSBPrinter(parts[1])
			}
		}
	}

	// Brief pause to let CUPS update status after probe
	time.Sleep(500 * time.Millisecond)

	// Re-read status after probe
	out, err = exec.Command("lpstat", "-p").CombinedOutput()
	if err != nil {
		return []PrinterInfo{}, nil
	}

	fullOutput := string(out)
	var printers []PrinterInfo
	for _, line := range strings.Split(fullOutput, "\n") {
		if strings.HasPrefix(line, "printer ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				name := parts[1]
				enabled := strings.Contains(line, "enabled") && !strings.Contains(line, "disabled")
				if strings.Contains(fullOutput, name) &&
					(strings.Contains(fullOutput, "offline") || strings.Contains(fullOutput, "not responding")) {
					printerSection := extractPrinterSection(fullOutput, name)
					if strings.Contains(printerSection, "offline") || strings.Contains(printerSection, "not responding") {
						enabled = false
					}
				}
				printers = append(printers, PrinterInfo{Name: name, Enabled: enabled})
			}
		}
	}
	return printers, nil
}

func extractPrinterSection(output, printerName string) string {
	var section strings.Builder
	inSection := false
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "printer "+printerName+" ") {
			inSection = true
		} else if strings.HasPrefix(line, "printer ") {
			inSection = false
		}
		if inSection {
			section.WriteString(line)
			section.WriteString("\n")
		}
	}
	return section.String()
}

func normalizePN(name string) string {
	return strings.NewReplacer(" ", "_", "-", "_").Replace(strings.ToLower(name))
}

func isNetworkPrinter(name string) bool {
	return strings.HasPrefix(name, "_") && strings.Contains(name, "_")
}

// getConnectedUSBPrinters returns currently connected USB printer names
func getConnectedUSBPrinters() map[string]bool {
	devices := map[string]bool{}

	switch runtime.GOOS {
	case "darwin", "linux":
		out, err := exec.Command("lpinfo", "-v").CombinedOutput()
		if err != nil {
			return devices
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "usb://") {
				parts := strings.SplitN(line, "usb://", 2)
				if len(parts) == 2 {
					uri := parts[1]
					if idx := strings.Index(uri, "?"); idx > 0 {
						uri = uri[:idx]
					}
					devices[uri] = true
					devices[normalizePN(uri)] = true
					uriParts := strings.SplitN(uri, "/", 2)
					if len(uriParts) == 2 {
						devices[uriParts[1]] = true
						devices[normalizePN(uriParts[1])] = true
					}
				}
			}
		}
	}

	return devices
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
		status, _ := item["PrinterStatus"].(float64)
		enabled := status == 0
		printers = append(printers, PrinterInfo{Name: name, Enabled: enabled})
	}
	return printers, nil
}

// ─── Print to USB/OS Printer ───────────────────────────────────────────────

func printToUSB(printerName string, data []byte) error {
	switch runtime.GOOS {
	case "darwin", "linux":
		tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("print-%d.raw", time.Now().UnixNano()))
		if err := os.WriteFile(tmpFile, data, 0644); err != nil {
			return fmt.Errorf("failed to write temp file: %w", err)
		}
		defer os.Remove(tmpFile)
		cmd := exec.Command("lp", "-d", printerName, "-o", "raw", tmpFile)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("lp error: %s — %s", err, string(out))
		}
	case "windows":
		if err := sendRawToPrinter(printerName, data); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return nil
}
