package main

// ─── Build-time Variables ──────────────────────────────────────────────────
// Override at compile time: go build -ldflags "-X main.Version=v2.0.0 -X main.DevMode=false"

// DevMode controls whether localhost origins are allowed.
// Set to "false" at build time for production releases.
var DevMode = "true"

// ─── Network ───────────────────────────────────────────────────────────────

const (
	DefaultPort        = 9120
	DefaultPrinterPort = 9100
	MinPort            = 1
	MaxPort            = 65535
)

// ─── Timeouts ──────────────────────────────────────────────────────────────

const (
	HTTPReadTimeout  = 15 // seconds
	HTTPWriteTimeout = 60 // seconds
	HTTPIdleTimeout  = 60 // seconds
	MaxHeaderSize    = 1 << 20 // 1 MB
	MaxBodySize      = 10 * 1024 * 1024 // 10 MB
	MaxDownloadSize  = 100 * 1024 * 1024 // 100 MB

	NetworkDialTimeout  = 15 // seconds
	NetworkWriteTimeout = 15 // seconds
	TestDialTimeout     = 5  // seconds
	ProbeTimeout        = 2  // seconds
	CertFetchTimeout    = 10 // seconds
	UpdateCheckTimeout  = 10 // seconds
	UpdateDownloadTimeout = 2 // minutes
	ShutdownTimeout     = 10 // seconds
)

// ─── Caching ───────────────────────────────────────────────────────────────

const (
	PrinterCacheTTLSeconds = 30
	UpdateCacheTTLHours    = 1
	CertRefreshHours       = 24
)

// ─── Paths & Labels ────────────────────────────────────────────────────────

const (
	AppName       = "NME Print Bridge"
	ConfigDirName = ".printbridge"
	ConfigFile    = "config.json"
	CertCacheFile = "cert-cache.json"
	LogFile       = "bridge.log"

	LaunchAgentLabel = "com.nme.print-bridge"
	SystemdService   = "print-bridge"
)

// ─── Localhost Origins (dev only) ──────────────────────────────────────────

var LocalhostOrigins = []string{
	"http://localhost:3000",
	"http://localhost:3001",
	"https://localhost:3000",
}

func isDevMode() bool {
	return DevMode == "true"
}

// ─── Trusted Download Hosts ────────────────────────────────────────────────

var TrustedDownloadPrefixes = []string{
	"https://github.com/",
	"https://objects.githubusercontent.com/",
}
