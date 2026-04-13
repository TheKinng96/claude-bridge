package tray

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"runtime"

	"github.com/getlantern/systray"
)

// generateIcon creates a 64x64 PNG icon at runtime — a blue circle with
// a white speech-bubble shape. Works on both macOS (menu bar) and Windows
// (notification area, which needs ≥32x32).
func generateIcon() []byte {
	const size = 64
	const cx, cy = size / 2, size / 2
	const radius = 28.0

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	blue := color.RGBA{R: 59, G: 130, B: 246, A: 255}   // Tailwind blue-500
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}

	// Draw blue circle
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) - float64(cx)
			dy := float64(y) - float64(cy)
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist <= radius {
				img.Set(x, y, blue)
			}
		}
	}

	// Draw small white "CB" text approximation (two dots for simplicity)
	// Actually, draw a simple chat bubble shape in white
	for y := cy - 8; y <= cy + 4; y++ {
		for x := cx - 10; x <= cx + 10; x++ {
			// Rounded rectangle for bubble
			rx := float64(x - cx)
			ry := float64(y - (cy - 2))
			if math.Abs(rx) <= 10 && math.Abs(ry) <= 6 {
				// Round corners
				cornerDist := 0.0
				if math.Abs(rx) > 7 && math.Abs(ry) > 3 {
					cornerDist = math.Sqrt(math.Pow(math.Abs(rx)-7, 2) + math.Pow(math.Abs(ry)-3, 2))
				}
				if cornerDist <= 3 {
					img.Set(x, y, white)
				}
			}
		}
	}
	// Bubble tail
	img.Set(cx-3, cy+5, white)
	img.Set(cx-4, cy+6, white)
	img.Set(cx-2, cy+5, white)
	img.Set(cx-5, cy+7, white)

	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
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
	systray.SetIcon(generateIcon())
	systray.SetTitle("Claude Bridge")
	systray.SetTooltip(fmt.Sprintf("Claude Bridge — localhost:%d", port))

	mOpen := systray.AddMenuItem("Open Dashboard", "Open Claude Bridge in your browser")
	systray.AddSeparator()
	mShutDown := systray.AddMenuItem("Shut Down", "Stop Claude Bridge and exit")

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
