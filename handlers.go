package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"time"
)

// ─── Handlers ──────────────────────────────────────────────────────────────

func handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"name":     AppName,
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

	addr := net.JoinHostPort(req.IP, strconv.Itoa(req.Port))
	conn, err := net.DialTimeout("tcp", addr, time.Duration(NetworkDialTimeout)*time.Second)
	if err != nil {
		writeJSON(w, 500, Response{Success: false, Error: fmt.Sprintf("Connection failed: %s", err.Error())})
		return
	}
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(time.Duration(NetworkWriteTimeout) * time.Second))
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
