package main

import (
	"embed"
	"log"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		addr := "127.0.0.1:7070"
		var tlsCert, tlsKey, password string
		for i := 2; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--addr":
				i++
				if i < len(os.Args) {
					addr = os.Args[i]
				}
			case "--tls-cert":
				i++
				if i < len(os.Args) {
					tlsCert = os.Args[i]
				}
			case "--tls-key":
				i++
				if i < len(os.Args) {
					tlsKey = os.Args[i]
				}
			case "--password":
				i++
				if i < len(os.Args) {
					password = os.Args[i]
				}
			}
		}
		if err := serve(addr, tlsCert, tlsKey, password); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Default: Wails GUI mode
	tmuxSvc := &TmuxService{}
	termSvc := NewTerminalService()
	chatSvc := NewChatService(nil)

	app := application.New(application.Options{
		Name:        "Makro",
		Description: "Multi-agent coding orchestrator",
		Services: []application.Service{
			application.NewService(tmuxSvc),
			application.NewService(termSvc),
			application.NewService(chatSvc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Makro",
		Width:            1200,
		Height:           800,
		MinWidth:         800,
		MinHeight:        500,
		BackgroundColour: application.NewRGB(30, 30, 30),
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		URL: "/",
	})

	termSvc.SetApp(app)
	chatSvc.SetApp(app)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
