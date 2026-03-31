package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
)

// ─── Handlers ──────────────────────────────────────────────────────────────

// activePollerPtr is set by main before the server starts, read by status handler.
var activePollerPtr atomic.Pointer[Poller]

func handleStatus(w http.ResponseWriter, _ *http.Request) {
	status := map[string]any{
		"name":     AppName,
		"version":  Version,
		"platform": runtime.GOOS,
		"arch":     runtime.GOARCH,
		"status":   "running",
	}

	if p := activePollerPtr.Load(); p != nil {
		processed, lastPoll := p.Stats()
		pollerInfo := map[string]any{
			"enabled":        true,
			"admin_api":      p.config.AdminAPIURL,
			"interval":       p.config.PollIntervalSeconds,
			"jobs_processed": processed,
		}
		if !lastPoll.IsZero() {
			pollerInfo["last_poll"] = lastPoll.Format(time.RFC3339)
		}
		status["poller"] = pollerInfo
	}

	writeJSON(w, 200, status)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, Response{Success: true, Message: "ok"})
}

func handleListPrinters(w http.ResponseWriter, _ *http.Request) {
	printers, err := listPrintersCached()
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
	if err := validateIP(req.IP); err != nil {
		writeJSON(w, 400, Response{Success: false, Error: err.Error()})
		return
	}
	if req.Port == 0 {
		req.Port = DefaultPrinterPort
	}
	if req.Port < MinPort || req.Port > MaxPort {
		writeJSON(w, 400, Response{Success: false, Error: fmt.Sprintf("port must be %d-%d", MinPort, MaxPort)})
		return
	}

	printData, err := decodeData(req.Data, req.Raw)
	if err != nil {
		writeJSON(w, 400, Response{Success: false, Error: err.Error()})
		return
	}
	if len(printData) == 0 {
		writeJSON(w, 400, Response{Success: false, Error: "No print data"})
		return
	}

	if err := tcpSend(req.IP, req.Port, printData); err != nil {
		writeJSON(w, 500, Response{Success: false, Error: err.Error()})
		return
	}

	writeJSON(w, 200, Response{Success: true, Message: fmt.Sprintf("Sent %d bytes to %s:%d", len(printData), req.IP, req.Port)})
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
	if err := validatePrinterName(req.Printer); err != nil {
		writeJSON(w, 400, Response{Success: false, Error: err.Error()})
		return
	}

	printData, err := decodeData(req.Data, req.Raw)
	if err != nil {
		writeJSON(w, 400, Response{Success: false, Error: err.Error()})
		return
	}
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

// ─── Config Endpoints ──────────────────────────────────────────────────────

type PollConfigReq struct {
	AdminAPIURL         string `json:"admin_api_url"`
	ServiceKey          string `json:"service_key"`
	PollEnabled         bool   `json:"poll_enabled"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

func handleGetPollConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := loadConfig()
	writeJSON(w, 200, Response{Success: true, Data: map[string]any{
		"admin_api_url":       cfg.AdminAPIURL,
		"poll_enabled":        cfg.PollEnabled,
		"poll_interval_seconds": cfg.PollIntervalSeconds,
		"has_service_key":     cfg.ServiceKey != "",
	}})
}

func handleSetPollConfig(w http.ResponseWriter, r *http.Request) {
	var req PollConfigReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, Response{Success: false, Error: "Invalid JSON"})
		return
	}
	if req.AdminAPIURL == "" || req.ServiceKey == "" {
		writeJSON(w, 400, Response{Success: false, Error: "admin_api_url and service_key are required"})
		return
	}
	if req.PollIntervalSeconds < MinPollInterval {
		req.PollIntervalSeconds = DefaultPollInterval
	}

	// Update config file
	cfg := loadConfig()
	cfg.AdminAPIURL = req.AdminAPIURL
	cfg.ServiceKey = req.ServiceKey
	cfg.PollEnabled = req.PollEnabled
	cfg.PollIntervalSeconds = req.PollIntervalSeconds
	saveConfig(cfg)

	// Restart poller with new config
	if old := activePollerPtr.Load(); old != nil {
		old.Stop()
		log.Println("[poller] Stopped old poller for config update")
	}

	if cfg.PollEnabled {
		poller := NewPoller(cfg)
		activePollerPtr.Store(poller)
		poller.Start()
		log.Printf("[poller] Started with new config — polling %s every %ds",
			cfg.AdminAPIURL, cfg.PollIntervalSeconds)
	} else {
		activePollerPtr.Store(nil)
		log.Println("[poller] Polling disabled via config update")
	}

	writeJSON(w, 200, Response{Success: true, Message: "Poll config updated"})
}

func handleDeletePollConfig(w http.ResponseWriter, _ *http.Request) {
	// Stop poller
	if old := activePollerPtr.Load(); old != nil {
		old.Stop()
		activePollerPtr.Store(nil)
		log.Println("[poller] Stopped — polling disabled")
	}

	// Clear polling config
	cfg := loadConfig()
	cfg.AdminAPIURL = ""
	cfg.ServiceKey = ""
	cfg.PollEnabled = false
	saveConfig(cfg)

	writeJSON(w, 200, Response{Success: true, Message: "Poll config cleared"})
}

// ─── Test Handler ──────────────────────────────────────────────────────────

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
	if err := validateIP(req.IP); err != nil {
		writeJSON(w, 400, Response{Success: false, Error: err.Error()})
		return
	}
	if req.Port == 0 {
		req.Port = DefaultPrinterPort
	}
	if req.Port < MinPort || req.Port > MaxPort {
		writeJSON(w, 400, Response{Success: false, Error: fmt.Sprintf("port must be %d-%d", MinPort, MaxPort)})
		return
	}

	addr := net.JoinHostPort(req.IP, strconv.Itoa(req.Port))
	conn, err := net.DialTimeout("tcp", addr, time.Duration(TestDialTimeout)*time.Second)
	if err != nil {
		writeJSON(w, 200, map[string]any{"success": true, "online": false, "error": err.Error()})
		return
	}
	conn.Close()
	writeJSON(w, 200, map[string]any{"success": true, "online": true})
}
