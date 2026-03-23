# NME Print Bridge — Windows Installer
# Usage: irm https://raw.githubusercontent.com/narbada-madhusudhan/nme-print-bridge/main/install.ps1 | iex

$ErrorActionPreference = "Stop"
$Repo = "narbada-madhusudhan/nme-print-bridge"
$InstallDir = "$env:LOCALAPPDATA\NME Print Bridge"
$ExePath = "$InstallDir\nme-print-bridge.exe"
$Binary = "print-bridge-windows-amd64.exe"
$URL = "https://github.com/$Repo/releases/latest/download/$Binary"

# Handle --uninstall
if ($args -contains "--uninstall" -or $args -contains "uninstall") {
    Write-Host "`n  Uninstalling NME Print Bridge..."
    if (Test-Path $ExePath) {
        & $ExePath --uninstall 2>$null
        Remove-Item $ExePath -Force
        Write-Host "  OK Uninstalled" -ForegroundColor Green
    } else {
        Write-Host "  Not installed at $ExePath"
    }
    exit 0
}

Write-Host ""
Write-Host "  =======================================" -ForegroundColor Cyan
Write-Host "     NME Print Bridge - Installer        " -ForegroundColor Cyan
Write-Host "  =======================================" -ForegroundColor Cyan
Write-Host ""

# Stop existing if upgrading
if (Test-Path $ExePath) {
    Write-Host "  -> Stopping existing installation..."
    & $ExePath --uninstall 2>$null
    Remove-Item $ExePath -Force
}

# Download
Write-Host "  -> Downloading latest release..."
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$ExePath = "$InstallDir\nme-print-bridge.exe"
Invoke-WebRequest -Uri $URL -OutFile $ExePath -UseBasicParsing
Unblock-File -Path $ExePath

if (-not (Test-Path $ExePath)) {
    Write-Host "  X Download failed" -ForegroundColor Red
    exit 1
}

Write-Host "  OK Downloaded to $ExePath" -ForegroundColor Green

# Install auto-start
Write-Host "  -> Setting up auto-start..."
& $ExePath --install

Write-Host ""
Write-Host "  =======================================" -ForegroundColor Green
Write-Host "  OK Installation complete!              " -ForegroundColor Green
Write-Host "                                         "
Write-Host "  NME Print Bridge is now running and    "
Write-Host "  will start automatically on login.     "
Write-Host "                                         "
Write-Host "  Status: http://localhost:9120          "
Write-Host "                                         "
Write-Host "  To uninstall:                          "
Write-Host "  & '$ExePath' --uninstall               "
Write-Host "  =======================================" -ForegroundColor Green
Write-Host ""
