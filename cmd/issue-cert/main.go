// issue-cert — CLI tool to issue hotel certificates for Print Bridge
//
// Usage:
//   go run cmd/issue-cert/main.go \
//     -key ROOT_PRIVATE_KEY_BASE64 \
//     -hotel-id godawari-resort \
//     -hotel-name "Godawari Riverside Resort" \
//     -origins "https://admin.godawariresort.com,https://pos.godawariresort.com" \
//     -days 365

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type CertPayload struct {
	HotelID        string   `json:"hotel_id"`
	HotelName      string   `json:"hotel_name"`
	AllowedOrigins []string `json:"allowed_origins"`
	IssuedAt       string   `json:"issued_at"`
	ExpiresAt      string   `json:"expires_at"`
}

type SignedCert struct {
	Payload   CertPayload `json:"payload"`
	Signature string      `json:"signature"`
}

func main() {
	privateKeyB64 := flag.String("key", os.Getenv("ROOT_PRIVATE_KEY"), "Root private key (base64)")
	hotelID := flag.String("hotel-id", "", "Hotel ID")
	hotelName := flag.String("hotel-name", "", "Hotel display name")
	origins := flag.String("origins", "", "Comma-separated allowed origins")
	days := flag.Int("days", 365, "Certificate validity in days")
	output := flag.String("output", "", "Output file (default: stdout)")
	flag.Parse()

	if *privateKeyB64 == "" {
		fmt.Fprintln(os.Stderr, "Error: -key or ROOT_PRIVATE_KEY env var required")
		os.Exit(1)
	}
	if *hotelID == "" || *hotelName == "" || *origins == "" {
		fmt.Fprintln(os.Stderr, "Error: -hotel-id, -hotel-name, and -origins are required")
		flag.Usage()
		os.Exit(1)
	}

	// Decode private key
	privKeyBytes, err := base64.StdEncoding.DecodeString(*privateKeyB64)
	if err != nil || len(privKeyBytes) != ed25519.PrivateKeySize {
		fmt.Fprintln(os.Stderr, "Error: invalid private key")
		os.Exit(1)
	}
	privKey := ed25519.PrivateKey(privKeyBytes)

	// Build payload
	now := time.Now().UTC()
	payload := CertPayload{
		HotelID:        *hotelID,
		HotelName:      *hotelName,
		AllowedOrigins: strings.Split(*origins, ","),
		IssuedAt:       now.Format(time.RFC3339),
		ExpiresAt:      now.Add(time.Duration(*days) * 24 * time.Hour).Format(time.RFC3339),
	}

	// Serialize and sign
	payloadBytes, _ := json.Marshal(payload)
	signature := ed25519.Sign(privKey, payloadBytes)

	cert := SignedCert{
		Payload:   payload,
		Signature: base64.StdEncoding.EncodeToString(signature),
	}

	certJSON, _ := json.MarshalIndent(cert, "", "  ")

	if *output != "" {
		if err := os.WriteFile(*output, certJSON, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Certificate written to %s\n", *output)
	} else {
		fmt.Println(string(certJSON))
	}
}
