package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ─── Auto-Start Install/Uninstall ──────────────────────────────────────────

func installAutoStart() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("cannot resolve executable path: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return installMacLaunchAgent(exePath)
	case "windows":
		return installWindowsStartup(exePath)
	case "linux":
		return installLinuxSystemd(exePath)
	default:
		return fmt.Errorf("auto-start not supported on %s — run the binary manually", runtime.GOOS)
	}
}

func uninstallAutoStart() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallMacLaunchAgent()
	case "windows":
		return uninstallWindowsStartup()
	case "linux":
		return uninstallLinuxSystemd()
	default:
		return fmt.Errorf("auto-start not supported on %s", runtime.GOOS)
	}
}

// ─── macOS ─────────────────────────────────────────────────────────────────

func installMacLaunchAgent(exePath string) error {
	home, _ := os.UserHomeDir()
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	plistPath := filepath.Join(plistDir, LaunchAgentLabel + ".plist")

	os.MkdirAll(plistDir, 0755)

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>`+LaunchAgentLabel+`</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/print-bridge.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/print-bridge.log</string>
</dict>
</plist>`, exePath)

	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return err
	}

	exec.Command("launchctl", "unload", plistPath).Run()
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("launchctl load failed: %w", err)
	}

	fmt.Printf("  ✓ Auto-start installed (macOS LaunchAgent)\n")
	fmt.Printf("  ✓ NME Print Bridge will start automatically on login\n")
	fmt.Printf("  ✓ Logs: /tmp/print-bridge.log\n")
	fmt.Printf("  ✓ Uninstall: %s --uninstall\n", exePath)
	return nil
}

func uninstallMacLaunchAgent() error {
	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel + ".plist")
	exec.Command("launchctl", "unload", plistPath).Run()
	os.Remove(plistPath)
	fmt.Println("  ✓ Auto-start removed")
	return nil
}

// ─── Windows ───────────────────────────────────────────────────────────────

func installWindowsStartup(exePath string) error {
	home, _ := os.UserHomeDir()
	startupDir := filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup")

	// Remove old .bat if upgrading from previous version
	oldBat := filepath.Join(startupDir, "NME Print Bridge.bat")
	os.Remove(oldBat)

	// Use .vbs instead of .bat — runs the exe with zero visible windows
	vbsPath := filepath.Join(startupDir, "NME Print Bridge.vbs")
	vbs := fmt.Sprintf("CreateObject(\"Wscript.Shell\").Run \"\"\"%s\"\"\", 0, False\r\n", exePath)

	if err := os.WriteFile(vbsPath, []byte(vbs), 0644); err != nil {
		return fmt.Errorf("failed to create startup script: %w", err)
	}

	fmt.Printf("  ✓ Auto-start installed (Windows Startup folder)\n")
	fmt.Printf("  ✓ NME Print Bridge will start automatically on login\n")
	fmt.Printf("  ✓ Uninstall: %s --uninstall\n", exePath)
	return nil
}

// migrateWindowsStartup removes legacy .bat and ensures .vbs auto-start exists.
func migrateWindowsStartup() {
	home, _ := os.UserHomeDir()
	startupDir := filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	batPath := filepath.Join(startupDir, "NME Print Bridge.bat")
	vbsPath := filepath.Join(startupDir, "NME Print Bridge.vbs")

	// Clean up temp restart VBS from auto-update
	if exePath, err := os.Executable(); err == nil {
		os.Remove(exePath + ".restart.vbs")
	}

	if _, err := os.Stat(batPath); err == nil {
		os.Remove(batPath)
		log.Println("[migrate] Removed legacy .bat startup file")
	}

	if _, err := os.Stat(vbsPath); os.IsNotExist(err) {
		exePath, err := os.Executable()
		if err != nil {
			return
		}
		exePath, _ = filepath.EvalSymlinks(exePath)
		vbs := fmt.Sprintf("CreateObject(\"Wscript.Shell\").Run \"\"\"%s\"\"\", 0, False\r\n", exePath)
		if err := os.WriteFile(vbsPath, []byte(vbs), 0644); err == nil {
			log.Println("[migrate] Created .vbs startup file")
		}
	}
}

func uninstallWindowsStartup() error {
	home, _ := os.UserHomeDir()
	startupDir := filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	os.Remove(filepath.Join(startupDir, "NME Print Bridge.vbs"))
	os.Remove(filepath.Join(startupDir, "NME Print Bridge.bat"))
	fmt.Println("  ✓ Auto-start removed")
	return nil
}

// ─── Linux ─────────────────────────────────────────────────────────────────

func installLinuxSystemd(exePath string) error {
	home, _ := os.UserHomeDir()
	serviceDir := filepath.Join(home, ".config", "systemd", "user")
	servicePath := filepath.Join(serviceDir, SystemdService + ".service")

	os.MkdirAll(serviceDir, 0755)

	service := fmt.Sprintf(`[Unit]
Description=NME Print Bridge — Thermal Printer Service
After=network.target

[Service]
ExecStart=%s
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, exePath)

	if err := os.WriteFile(servicePath, []byte(service), 0644); err != nil {
		return err
	}

	exec.Command("systemctl", "--user", "daemon-reload").Run()
	exec.Command("systemctl", "--user", "enable", SystemdService).Run()
	if err := exec.Command("systemctl", "--user", "start", SystemdService).Run(); err != nil {
		return fmt.Errorf("systemctl start failed: %w", err)
	}

	fmt.Printf("  ✓ Auto-start installed (systemd user service)\n")
	fmt.Printf("  ✓ NME Print Bridge will start automatically on login\n")
	fmt.Printf("  ✓ Uninstall: %s --uninstall\n", exePath)
	return nil
}

func uninstallLinuxSystemd() error {
	exec.Command("systemctl", "--user", "stop", SystemdService).Run()
	exec.Command("systemctl", "--user", "disable", SystemdService).Run()
	home, _ := os.UserHomeDir()
	os.Remove(filepath.Join(home, ".config", "systemd", "user", SystemdService + ".service"))
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Println("  ✓ Auto-start removed")
	return nil
}
