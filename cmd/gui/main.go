package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	tmuxSvc := &TmuxService{}
	termSvc := NewTerminalService()
	chatSvc := NewChatService(nil)

	app := application.New(application.Options{
		Name:        "FingerSaver",
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
		Title:            "FingerSaver",
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

	err := app.Run()
	if err != nil {
		log.Fatal(err)
	}
}
