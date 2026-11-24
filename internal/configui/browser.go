package configui

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
)

// OpenBrowser launches the default browser for the UI address.
func OpenBrowser(addr string) error {
	uiURL, err := BrowserURL(addr)
	if err != nil {
		return err
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", uiURL)
	case "darwin":
		cmd = exec.Command("open", uiURL)
	default:
		cmd = exec.Command("xdg-open", uiURL)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}

// BrowserURL converts a listen address into a browser-safe loopback URL.
func BrowserURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("parse UI address: %w", err)
	}

	switch strings.Trim(host, "[]") {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	case "localhost":
		// Preserve localhost for browser display.
	default:
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			host = ip.String()
		} else if ip := net.ParseIP(strings.Trim(host, "[]")); ip == nil {
			host = "127.0.0.1"
		}
	}

	if strings.Contains(host, ":") {
		return fmt.Sprintf("http://[%s]:%s", strings.Trim(host, "[]"), port), nil
	}
	return fmt.Sprintf("http://%s:%s", host, port), nil
}
