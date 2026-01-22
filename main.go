package main

import (
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Echo all incoming binary messages back immediately
func wsEcho(c echo.Context) error {
	ws, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer ws.Close()

	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			break
		}
		// Echo back instantly
		if err := ws.WriteMessage(websocket.BinaryMessage, message); err != nil {
			break
		}
	}
	return nil
}

func main() {
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	e.GET("/ws-echo", wsEcho)
	e.Static("/", "public")

	println("🎧 Echo server running on http://localhost:8080")
	e.Logger.Fatal(e.Start(":8080"))
}
