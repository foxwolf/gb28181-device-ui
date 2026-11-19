package main

import (
	"log/slog"
	"os"

	"gioui.org/app"

	"gb28181-device-ui/internal/ui"
)

func main() {
	u := ui.New()
	slog.SetDefault(slog.New(u.Handler()))

	go func() {
		if err := u.Run(); err != nil {
			slog.Error("窗口退出", "error", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}
