# Print Bridge

Lightweight thermal printer bridge for web-based POS systems. Single binary, zero dependencies, zero dialogs.

Connects web browsers to thermal printers via a localhost HTTP API. Supports network printers (TCP/ESC-POS) and USB/OS printers.

## Download

| Platform | Download |
|----------|----------|
| **Windows** | [print-bridge-windows-amd64.exe](https://github.com/narbada-madhusudhan/print-bridge/releases/latest/download/print-bridge-windows-amd64.exe) |
| **macOS (Apple Silicon)** | [print-bridge-mac-arm64](https://github.com/narbada-madhusudhan/print-bridge/releases/latest/download/print-bridge-mac-arm64) |
| **macOS (Intel)** | [print-bridge-mac-amd64](https://github.com/narbada-madhusudhan/print-bridge/releases/latest/download/print-bridge-mac-amd64) |

## Quick Start

### Windows
1. Download `print-bridge-windows-amd64.exe`
2. Double-click to run
3. Done — Print Bridge is running on `http://localhost:9120`

### macOS
```bash
chmod +x print-bridge-mac-arm64
./print-bridge-mac-arm64
```

### Configure for your hotel
```bash
./print-bridge --hotel-id your-hotel-id
```

## API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Status + info |
| `/health` | GET | Health check |
| `/printers` | GET | List installed printers |
| `/print/network` | POST | Print to network printer (TCP) |
| `/print/usb` | POST | Print to USB/OS printer |
| `/test` | POST | Test printer connectivity |

### Print to network printer
```bash
curl -X POST http://localhost:9120/print/network \
  -H "Content-Type: application/json" \
  -d '{"ip":"192.168.1.100","port":9100,"raw":"Hello printer!\n"}'
```

### List printers
```bash
curl http://localhost:9120/printers
```

## Security

Print Bridge uses certificate-based trust. Each hotel gets a signed certificate that specifies which web origins can connect. The certificate is verified against a baked-in root public key.

- Only whitelisted origins can make requests (CORS)
- `localhost:3000` and `localhost:3001` are always allowed for development
- Certificates expire and can be rotated without updating the binary

## Build from source

```bash
go build -o print-bridge main.go
```

### Issue a hotel certificate
```bash
go run cmd/issue-cert/main.go \
  -key $ROOT_PRIVATE_KEY \
  -hotel-id my-hotel \
  -hotel-name "My Hotel" \
  -origins "https://admin.myhotel.com,https://pos.myhotel.com" \
  -days 365
```
