package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
	// Enable per-message deflate compression (matches FreeSWITCH default)
	EnableCompression: true,
}

type StreamMetadata struct {
	CallID       string `json:"call_id"`
	CallerNumber string `json:"caller_number"`
	SampleRate   int    `json:"sample_rate"`
	Channels     int    `json:"channels"`
}

// Handle FreeSWITCH audio streaming
func wsFreeSwitchEcho(c echo.Context) error {
	ws, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer ws.Close()

	log.Printf("New FreeSWITCH connection from %s", c.Request().RemoteAddr)

	// Set read/write deadlines to prevent hanging connections
	ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	ws.SetWriteDeadline(time.Now().Add(10 * time.Second))

	// Optional: Send initial connection acknowledgment
	ackMsg := map[string]string{
		"status":  "ready",
		"message": "Echo server ready to receive audio",
	}
	ackJSON, _ := json.Marshal(ackMsg)
	if err := ws.WriteMessage(websocket.TextMessage, ackJSON); err != nil {
		log.Printf("Error sending ack: %v", err)
	}

	messageCount := 0

	for {
		// Reset read deadline on each message
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))

		messageType, message, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("WebSocket error: %v", err)
			} else {
				log.Printf("Connection closed normally")
			}
			break
		}

		messageCount++

		switch messageType {
		case websocket.TextMessage:
			// Handle metadata or text messages from FreeSWITCH
			log.Printf("Received text message: %s", string(message))

			// Try to parse as metadata
			var metadata StreamMetadata
			if err := json.Unmarshal(message, &metadata); err == nil {
				log.Printf("Stream metadata - CallID: %s, Caller: %s, SampleRate: %d, Channels: %d",
					metadata.CallID, metadata.CallerNumber, metadata.SampleRate, metadata.Channels)
			}

			// Send acknowledgment
			response := map[string]interface{}{
				"status":    "received",
				"type":      "metadata",
				"timestamp": time.Now().Unix(),
			}
			responseJSON, _ := json.Marshal(response)
			if err := ws.WriteMessage(websocket.TextMessage, responseJSON); err != nil {
				log.Printf("Error sending text response: %v", err)
				break
			}

		case websocket.BinaryMessage:
			// This is L16 audio data from FreeSWITCH
			audioSize := len(message)

			// Log every 100th message to avoid flooding
			if messageCount%100 == 0 {
				log.Printf("Received audio chunk #%d: %d bytes", messageCount, audioSize)
			}

			// Echo the audio back immediately
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.WriteMessage(websocket.BinaryMessage, message); err != nil {
				log.Printf("Error echoing audio: %v", err)
				break
			}

			// Optional: Send periodic JSON status updates
			// Uncomment if you want FreeSWITCH to receive status updates
			/*
			   if messageCount%200 == 0 {
			       statusMsg := map[string]interface{}{
			           "type": "status",
			           "message": "Processing audio",
			           "packets_received": messageCount,
			           "timestamp": time.Now().Unix(),
			       }
			       statusJSON, _ := json.Marshal(statusMsg)
			       ws.WriteMessage(websocket.TextMessage, statusJSON)
			   }
			*/
		}
	}

	log.Printf("Connection closed. Total messages processed: %d", messageCount)
	return nil
}

// Optional: Endpoint to play audio back to caller
// This demonstrates how to send audio from server to FreeSWITCH
func wsFreeSwitchPlayback(c echo.Context) error {
	ws, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer ws.Close()

	log.Printf("New playback connection from %s", c.Request().RemoteAddr)

	// Read metadata
	_, metadataMsg, err := ws.ReadMessage()
	if err != nil {
		log.Printf("Error reading metadata: %v", err)
		return err
	}

	log.Printf("Playback metadata: %s", string(metadataMsg))

	// Process incoming audio and send back processed audio
	for {
		messageType, message, err := ws.ReadMessage()
		if err != nil {
			break
		}

		if messageType == websocket.BinaryMessage {
			// Here you could:
			// 1. Process the audio (speech recognition, etc.)
			// 2. Generate response audio
			// 3. Send it back using the "streamAudio" format

			// Example: Send audio back to be played
			// (This would need actual audio data in base64)
			/*
			   playbackResponse := map[string]interface{}{
			       "type": "streamAudio",
			       "data": map[string]interface{}{
			           "audioDataType": "raw",
			           "sampleRate":    8000,
			           "audioData":     base64.StdEncoding.EncodeToString(processedAudioBytes),
			       },
			   }
			   responseJSON, _ := json.Marshal(playbackResponse)
			   ws.WriteMessage(websocket.TextMessage, responseJSON)
			*/

			// For now, just echo
			ws.WriteMessage(websocket.BinaryMessage, message)
		}
	}

	return nil
}

// Health check endpoint
func healthCheck(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"status":  "healthy",
		"service": "freeswitch-echo-server",
	})
}

func main() {
	e := echo.New()

	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())

	// Routes
	e.GET("/health", healthCheck)
	e.GET("/ws-freeswitch", wsFreeSwitchEcho)
	e.GET("/ws-playback", wsFreeSwitchPlayback)

	// Optional: Serve static files if needed
	// e.Static("/", "public")

	port := ":8080"
	log.Printf("🎧 FreeSWITCH Echo Server running on http://localhost%s", port)
	log.Printf("WebSocket endpoint: ws://localhost%s/ws-freeswitch", port)
	log.Println("Ready to receive L16 audio streams from FreeSWITCH")

	e.Logger.Fatal(e.Start(port))
}
