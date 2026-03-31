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
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// Version is set at build time via: go build -ldflags "-X main.Version=vX.Y.Z"
var Version = "dev"


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

// ─── Main ──────────────────────────────────────────────────────────────────

func main() {
	// On Windows (-H windowsgui), stdout/stderr go nowhere.
	// Write logs to a file so errors are diagnosable.
	if runtime.GOOS == "windows" {
		os.MkdirAll(configDir(), 0755)
		logPath := filepath.Join(configDir(), LogFile)
		if logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
			log.SetOutput(logFile)
			os.Stdout = logFile
			os.Stderr = logFile
		}
	}

	hotelID := flag.String("hotel-id", "", "Hotel ID for certificate lookup")
	certURL := flag.String("cert-url", "", "Certificate API URL")
	adminAPIURL := flag.String("admin-api-url", "", "Admin API URL for print job polling")
	branchID := flag.String("branch-id", "", "Restaurant branch ID for print job polling")
	serviceKey := flag.String("service-key", "", "Service key for admin API authentication")
	poll := flag.Bool("poll", false, "Enable background print job polling")
	install := flag.Bool("install", false, "Install auto-start (runs on login)")
	uninstall := flag.Bool("uninstall", false, "Remove auto-start")
	flag.Parse()

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

	if runtime.GOOS == "windows" {
		migrateWindowsStartup()
	}

	cfg := loadConfig()
	if *hotelID != "" {
		cfg.HotelID = *hotelID
	}
	if *certURL != "" {
		cfg.CertURL = *certURL
	}
	if *adminAPIURL != "" {
		cfg.AdminAPIURL = *adminAPIURL
	}
	if *branchID != "" {
		cfg.RestaurantBranchID = *branchID
	}
	if *serviceKey != "" {
		cfg.ServiceKey = *serviceKey
	}
	if *poll {
		cfg.PollEnabled = true
	}
	saveConfig(cfg)

	cm, err := NewCertManager(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize: %v", err)
	}

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
	mux.HandleFunc("/config/poll", corsMiddleware(cm, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			handleGetPollConfig(w, r)
		case "POST":
			handleSetPollConfig(w, r)
		case "DELETE":
			handleDeletePollConfig(w, r)
		default:
			writeJSON(w, 405, Response{Success: false, Error: "Method not allowed"})
		}
	}))

	addr := fmt.Sprintf("127.0.0.1:%d", DefaultPort)

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

	// Start background print job poller (activePoller is read by status handler)
	var poller *Poller
	if cfg.PollEnabled && cfg.AdminAPIURL != "" && cfg.ServiceKey != "" {
		poller = NewPoller(cfg)
		activePollerPtr.Store(poller)
		poller.Start()
		fmt.Printf("  Poller: ON (every %ds → %s)\n", cfg.PollIntervalSeconds, cfg.AdminAPIURL)
		log.Printf("[poller] Started — polling %s every %ds",
			cfg.AdminAPIURL, cfg.PollIntervalSeconds)
	}

	// Auto-update on startup
	go func() {
		time.Sleep(3 * time.Second)
		info, err := checkForUpdate()
		if err != nil || !info.Available {
			return
		}
		if info.DownloadURL == "" {
			log.Printf("[update] Update %s available but no download URL for this platform", info.LatestVersion)
			return
		}
		log.Printf("[update] Auto-updating: %s → %s", info.CurrentVersion, info.LatestVersion)
		if err := performUpdate(info.DownloadURL); err != nil {
			log.Printf("[update] Auto-update failed: %v", err)
		}
	}()

	srv := &http.Server{
		Addr:           addr,
		Handler:        mux,
		ReadTimeout:    time.Duration(HTTPReadTimeout) * time.Second,
		WriteTimeout:   time.Duration(HTTPWriteTimeout) * time.Second,
		IdleTimeout:    time.Duration(HTTPIdleTimeout) * time.Second,
		MaxHeaderBytes: MaxHeaderSize,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		log.Println("[server] Shutting down gracefully...")
		if poller != nil {
			poller.Stop()
			log.Println("[poller] Stopped")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(ShutdownTimeout)*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	fmt.Println()
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Println("[server] Stopped.")
}
