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
		if len(os.Args) > 3 && os.Args[2] == "--addr" {
			addr = os.Args[3]
		}
		if err := serve(addr); err != nil {
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
