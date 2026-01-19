package main

import (
	"fmt"
	"net/http"
	"os/exec"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// 🔴 REPLACE THIS WITH YOUR PUBLIC S3 MP3 URL
const s3MP3URL = "https://s3.us-east-2.amazonaws.com/resources.rvmstg.leadgg.com/account/68bd97967d88c9e213556deb/soundfile/68cc1a0dfb75a5192808e660/Pink_Panther.mp3"

var pcmData []byte // Raw PCM: s16le, 44100 Hz, stereo

func init() {
	fmt.Println("📥 Downloading MP3 from S3...")
	resp, err := http.Get(s3MP3URL)
	if err != nil {
		panic(fmt.Errorf("failed to download MP3: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		panic(fmt.Errorf("S3 returned status %d", resp.StatusCode))
	}

	// Convert MP3 → raw PCM using ffmpeg
	fmt.Println("🔄 Converting MP3 to PCM...")
	cmd := exec.Command("ffmpeg",
		"-i", "pipe:0",
		"-f", "s16le",
		"-ar", "44100",
		"-ac", "2",
		"pipe:1",
	)
	cmd.Stdin = resp.Body

	pcmData, err = cmd.Output()
	if err != nil {
		panic(fmt.Errorf("ffmpeg failed: %w", err))
	}

	samples := len(pcmData) / 4 // 2 channels * 2 bytes
	duration := float64(samples) / 44100.0
	fmt.Printf("✅ PCM ready: %d bytes (%.1f seconds)\n", len(pcmData), duration)
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Send full PCM buffer once
func wsFull(c echo.Context) error {
	ws, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer ws.Close()

	// Send entire PCM in one binary message
	err = ws.WriteMessage(websocket.BinaryMessage, pcmData)
	if err != nil {
		return nil
	}

	// Keep connection open briefly to avoid immediate close
	select {}
}

func main() {
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	// Routes
	e.GET("/ws-full", wsFull)
	e.Static("/", "public")

	fmt.Println("🎧 Server running on http://localhost:8080")
	e.Logger.Fatal(e.Start(":8080"))
}
