# NME Print Bridge — Windows Installer
# Usage: irm https://raw.githubusercontent.com/narbada-madhusudhan/nme-print-bridge/main/install.ps1 | iex

function Install-PrintBridge {
    $ErrorActionPreference = "Stop"
    $Repo = "narbada-madhusudhan/nme-print-bridge"
    $InstallDir = "$env:LOCALAPPDATA\NME Print Bridge"
    $ExePath = "$InstallDir\nme-print-bridge.exe"
    $Binary = "print-bridge-windows-amd64.exe"
    $URL = "https://github.com/$Repo/releases/latest/download/$Binary"

    # ── Helpers ──────────────────────────────────────────────────────────

    function Stop-Bridge {
        # Kill all running instances — use per-cmdlet ErrorAction to avoid
        # terminating errors when process doesn't exist
        Get-Process -Name "nme-print-bridge" -ErrorAction SilentlyContinue |
            Stop-Process -Force -ErrorAction SilentlyContinue
        # Also kill by path in case process name is mangled
        Get-Process -ErrorAction SilentlyContinue |
            Where-Object { $_.Path -like "*nme-print-bridge*" } |
            Stop-Process -Force -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 1
    }

    function Remove-Bridge {
        Remove-Item $ExePath -Force -ErrorAction SilentlyContinue
        if (Test-Path $ExePath) {
            # File locked — rename so we can download fresh (cleaned up later)
            $OldPath = "$ExePath.old"
            Remove-Item $OldPath -Force -ErrorAction SilentlyContinue
            Rename-Item $ExePath $OldPath -Force -ErrorAction SilentlyContinue
        }
    }

    # ── Main ─────────────────────────────────────────────────────────────

    Write-Host ""
    Write-Host "  =======================================" -ForegroundColor Cyan
    Write-Host "     NME Print Bridge - Installer        " -ForegroundColor Cyan
    Write-Host "  =======================================" -ForegroundColor Cyan
    Write-Host ""

    # Always kill any running bridge process and clean up legacy .bat first
    $StartupDir = "$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup"
    Write-Host "  -> Stopping any running bridge..."
    Stop-Bridge
    # Remove legacy .bat that causes PowerShell window flash on boot
    Remove-Item "$StartupDir\NME Print Bridge.bat" -Force -ErrorAction SilentlyContinue

    # Stop existing if upgrading
    if (Test-Path $ExePath) {
        try { & $ExePath --uninstall 2>$null } catch {}
        Stop-Bridge
        Remove-Bridge
        if ((Test-Path $ExePath) -and -not (Test-Path "$ExePath.old")) {
            Write-Host "  X Could not remove old binary. Close any running instances and retry." -ForegroundColor Red
            return
        }
    }

    # Clean up old renamed binary
    Remove-Item "$ExePath.old" -Force -ErrorAction SilentlyContinue

    # Download
    Write-Host "  -> Downloading latest release..."
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    try {
        Invoke-WebRequest -Uri $URL -OutFile $ExePath -UseBasicParsing
    } catch {
        Write-Host "  X Download failed: $_" -ForegroundColor Red
        return
    }
    Unblock-File -Path $ExePath

    if (-not (Test-Path $ExePath)) {
        Write-Host "  X Download failed" -ForegroundColor Red
        return
    }

    Write-Host "  OK Downloaded to $ExePath" -ForegroundColor Green

    # Install auto-start (creates .vbs in Startup — no window flash)
    Write-Host "  -> Setting up auto-start..."
    try { & $ExePath --install } catch {
        Write-Host "  ! Auto-start setup failed: $_" -ForegroundColor Yellow
    }

    # Start the bridge completely hidden — use VBS to avoid any window flash
    Write-Host "  -> Starting bridge..."
    $VbsLauncher = "$InstallDir\launch.vbs"
    Set-Content -Path $VbsLauncher -Value "CreateObject(""Wscript.Shell"").Run """"""$ExePath"""""", 0, False"
    wscript.exe $VbsLauncher
    Remove-Item $VbsLauncher -Force -ErrorAction SilentlyContinue

    # Verify the bridge is actually running
    Write-Host "  -> Verifying bridge is running..."
    $Running = $false
    for ($i = 0; $i -lt 5; $i++) {
        Start-Sleep -Seconds 1
        try {
            $Response = Invoke-WebRequest -Uri "http://localhost:9120/health" -UseBasicParsing -TimeoutSec 3
            if ($Response.StatusCode -eq 200) {
                $Running = $true
                break
            }
        } catch {
            # Not ready yet, retry
        }
    }

    # Get installed version from health endpoint
    $Version = "unknown"
    if ($Running) {
        try {
            $Health = Invoke-WebRequest -Uri "http://localhost:9120/" -UseBasicParsing -TimeoutSec 3
            $VersionMatch = [regex]::Match($Health.Content, '"version"\s*:\s*"([^"]+)"')
            if ($VersionMatch.Success) { $Version = $VersionMatch.Groups[1].Value }
        } catch {}
    }

    Write-Host ""
    if ($Running) {
        Write-Host "  =======================================" -ForegroundColor Green
        Write-Host "  OK Installation complete!              " -ForegroundColor Green
        Write-Host "  Version: $Version" -ForegroundColor Green
        Write-Host "                                         "
        Write-Host "  NME Print Bridge is now running and    "
        Write-Host "  will start automatically on login.     "
        Write-Host "                                         "
        Write-Host "  Status: http://localhost:9120          "
        Write-Host "                                         "
        Write-Host "  To uninstall:                          "
        Write-Host "  & '$ExePath' --uninstall               "
        Write-Host "  =======================================" -ForegroundColor Green
    } else {
        Write-Host "  =======================================" -ForegroundColor Yellow
        Write-Host "  ! Installation finished but bridge     " -ForegroundColor Yellow
        Write-Host "    did not respond on port 9120.        " -ForegroundColor Yellow
        Write-Host "                                         "
        Write-Host "  Try running manually to see errors:    "
        Write-Host "  & '$ExePath'                           "
        Write-Host "                                         "
        Write-Host "  Common fixes:                          "
        Write-Host "  - Allow through Windows Firewall       "
        Write-Host "  - Allow in Windows Defender/antivirus  "
        Write-Host "  - Run PowerShell as Administrator      "
        Write-Host "  =======================================" -ForegroundColor Yellow
    }
    Write-Host ""
}

# Run the installer
Install-PrintBridge
