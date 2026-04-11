package tray

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/getlantern/systray"
)

// iconPNG is a 22x22 blue speech-bubble icon embedded as raw PNG bytes.
// Shows in macOS menu bar (top) and Windows notification area (bottom-right).
var iconPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x16, 0x00, 0x00, 0x00, 0x16,
	0x08, 0x06, 0x00, 0x00, 0x00, 0xc4, 0xb4, 0x6c, 0x3b, 0x00, 0x00, 0x00,
	0x38, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x60, 0x18, 0x05, 0xd8,
	0x80, 0xb5, 0xff, 0x8d, 0xff, 0x94, 0x60, 0x9a, 0x18, 0x8a, 0xd3, 0xf0,
	0x51, 0x83, 0x47, 0x0d, 0x1e, 0x35, 0x18, 0x8f, 0xc1, 0xd4, 0x30, 0x1c,
	0xab, 0xa1, 0x84, 0x2c, 0x20, 0xa8, 0x89, 0x18, 0x40, 0x75, 0x03, 0x91,
	0x0d, 0xa6, 0xaa, 0x81, 0xa3, 0x80, 0x26, 0x00, 0x00, 0x45, 0x01, 0x0d,
	0xdc, 0x1f, 0xc1, 0x2e, 0x88, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
	0x44, 0xae, 0x42, 0x60, 0x82,
}

// Run starts the system tray icon. This function blocks until the user
// clicks "Shut Down" or the tray exits.
//
//   - macOS: appears in the top menu bar
//   - Windows: appears in the bottom-right notification area
//
// onQuit is called before the process exits so you can clean up
// (disconnect WhatsApp, stop the HTTP server, etc.).
func Run(port int, onQuit func()) {
	systray.Run(func() {
		onReady(port, onQuit)
	}, func() {
		// onExit — called after systray.Quit()
	})
}

func onReady(port int, onQuit func()) {
	systray.SetIcon(iconPNG)
	systray.SetTitle("CRM Agent")
	systray.SetTooltip(fmt.Sprintf("CRM Agent — localhost:%d", port))

	mOpen := systray.AddMenuItem("Open Dashboard", "Open CRM Agent in your browser")
	systray.AddSeparator()
	mShutDown := systray.AddMenuItem("Shut Down", "Stop CRM Agent and exit")

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser(fmt.Sprintf("http://127.0.0.1:%d", port))
			case <-mShutDown.ClickedCh:
				if onQuit != nil {
					onQuit()
				}
				systray.Quit()
				os.Exit(0)
			}
		}
	}()
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
