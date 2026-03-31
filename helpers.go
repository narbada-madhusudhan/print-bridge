package main

import (
	"encoding/base64"
	"fmt"
	"net"
)

// ─── Data Helpers ──────────────────────────────────────────────────────────

func decodeData(b64, raw string) ([]byte, error) {
	if b64 != "" {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 data: %w", err)
		}
		return data, nil
	}
	return []byte(raw), nil
}

func validateIP(ip string) error {
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP address: %s", ip)
	}
	return nil
}
