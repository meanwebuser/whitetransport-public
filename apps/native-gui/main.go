package main

import (
	"context"
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	launchOptions, err := parseLaunchOptions(os.Args[1:])
	if err != nil {
		println("Error:", err.Error())
		return
	}
	if err := loadTestSudoCredential(launchOptions); err != nil {
		println("Error:", err.Error())
		return
	}
	app, err := NewDefaultApp()
	if err != nil {
		println("Error:", err.Error())
		return
	}

	err = wails.Run(&options.App{
		Title:     "WhiteTransport",
		Width:     980,
		Height:    720,
		MinWidth:  720,
		MinHeight: 560,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 246, G: 247, B: 249, A: 1},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
		},
		OnDomReady: func(ctx context.Context) {
			if launchOptions.enabled() {
				go func() {
					app.runLaunchConnectTest(launchOptions)
					if launchOptions.testExit {
						wailsruntime.Quit(ctx)
					}
				}()
			}
		},
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
