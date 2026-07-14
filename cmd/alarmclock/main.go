package main

import (
	"fmt"
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

func main() {
	a := app.NewWithID("gr.polaris.claudealarm")
	w := a.NewWindow("Claude Alarm Clock — spike")
	w.SetContent(widget.NewLabel("Close this window. The process must stay alive.\nThen quit from the tray icon."))
	w.Resize(fyne.NewSize(420, 120))

	iconBytes, err := os.ReadFile("assets/icon.png")
	if err != nil {
		panic(err)
	}
	icon := fyne.NewStaticResource("icon.png", iconBytes)

	if desk, ok := a.(desktop.App); ok {
		quit := fyne.NewMenuItem("Quit", func() {
			fmt.Println("TEARDOWN RAN")
			a.Quit()
		})
		quit.IsQuit = true // else Fyne appends its own Quit, skipping our teardown
		desk.SetSystemTrayMenu(fyne.NewMenu("Claude Alarm",
			fyne.NewMenuItem("Show", func() { w.Show(); w.RequestFocus() }),
			fyne.NewMenuItemSeparator(),
			quit,
		))
		desk.SetSystemTrayIcon(icon)
		desk.SetSystemTrayWindow(w)
	} else {
		fmt.Println("WARNING: not a desktop.App — no tray")
	}

	// The single line keeping the process alive when the user clicks X.
	w.SetCloseIntercept(func() {
		fmt.Println("close intercepted -> hiding")
		w.Hide()
	})

	w.ShowAndRun()
	fmt.Println("ShowAndRun returned cleanly")
}
