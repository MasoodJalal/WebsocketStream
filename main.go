package main

import (
	"encoding/base64"
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

// FreeSWITCH streamAudio response format
type StreamAudioResponse struct {
	Type string          `json:"type"`
	Data StreamAudioData `json:"data"`
}

type StreamAudioData struct {
	AudioDataType string `json:"audioDataType"` // raw, wav, mp3, ogg
	SampleRate    int    `json:"sampleRate"`    // 8000 or 16000
	AudioData     string `json:"audioData"`     // base64 encoded audio
}

// Handle FreeSWITCH audio streaming with proper playback format
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

	messageCount := 0
	currentSampleRate := 8000 // Default, will be updated from metadata

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

				// Update current sample rate from metadata
				if metadata.SampleRate > 0 {
					currentSampleRate = metadata.SampleRate
				}
			}

			// Send acknowledgment
			response := map[string]interface{}{
				"status":    "received",
				"type":      "metadata",
				"timestamp": time.Now().Unix(),
			}
			responseJSON, _ := json.Marshal(response)
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.WriteMessage(websocket.TextMessage, responseJSON); err != nil {
				log.Printf("Error sending text response: %v", err)
				break
			}

		case websocket.BinaryMessage:
			// This is L16 audio data from FreeSWITCH
			audioSize := len(message)

			// Log every 100th message to avoid flooding
			if messageCount%100 == 0 {
				log.Printf("Received audio chunk #%d: %d bytes (will echo back via streamAudio)", messageCount, audioSize)
			}

			// Encode audio to base64
			audioBase64 := base64.StdEncoding.EncodeToString(message)

			// Create the streamAudio response in the format FreeSWITCH expects
			streamResponse := StreamAudioResponse{
				Type: "streamAudio",
				Data: StreamAudioData{
					AudioDataType: "raw", // L16 raw audio
					SampleRate:    currentSampleRate,
					AudioData:     audioBase64,
				},
			}

			// Convert to JSON
			responseJSON, err := json.Marshal(streamResponse)
			if err != nil {
				log.Printf("Error marshaling streamAudio response: %v", err)
				continue
			}

			// Send the JSON response back to FreeSWITCH
			// FreeSWITCH will decode the base64 audio and play it back to the caller
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.WriteMessage(websocket.TextMessage, responseJSON); err != nil {
				log.Printf("Error sending streamAudio response: %v", err)
				break
			}

			// Optional: Send periodic status updates
			if messageCount%200 == 0 {
				statusMsg := map[string]interface{}{
					"type":             "status",
					"message":          "Processing audio",
					"packets_received": messageCount,
					"timestamp":        time.Now().Unix(),
				}
				statusJSON, _ := json.Marshal(statusMsg)
				ws.WriteMessage(websocket.TextMessage, statusJSON)
			}
		}
	}

	log.Printf("Connection closed. Total messages processed: %d", messageCount)
	return nil
}

// Example: Advanced handler that could process audio before echoing
func wsFreeSwitchProcessed(c echo.Context) error {
	ws, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer ws.Close()

	log.Printf("New FreeSWITCH processed audio connection from %s", c.Request().RemoteAddr)

	currentSampleRate := 8000

	for {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))

		messageType, message, err := ws.ReadMessage()
		if err != nil {
			break
		}

		switch messageType {
		case websocket.TextMessage:
			var metadata StreamMetadata
			if err := json.Unmarshal(message, &metadata); err == nil {
				if metadata.SampleRate > 0 {
					currentSampleRate = metadata.SampleRate
				}
				log.Printf("Metadata received - SampleRate: %d", currentSampleRate)
			}

		case websocket.BinaryMessage:
			// Here you could:
			// 1. Send audio to ASR service (Google Speech, Watson, etc.)
			// 2. Process the audio (noise reduction, volume adjustment, etc.)
			// 3. Generate response audio from TTS
			// 4. Send processed audio back

			// For now, just echo with the proper format
			audioBase64 := base64.StdEncoding.EncodeToString(message)

			streamResponse := StreamAudioResponse{
				Type: "streamAudio",
				Data: StreamAudioData{
					AudioDataType: "raw",
					SampleRate:    currentSampleRate,
					AudioData:     audioBase64,
				},
			}

			responseJSON, _ := json.Marshal(streamResponse)
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			ws.WriteMessage(websocket.TextMessage, responseJSON)
		}
	}

	return nil
}

// Health check endpoint
func healthCheck(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"status":  "healthy",
		"service": "freeswitch-echo-server",
		"port":    "12000",
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
	e.GET("/ws-processed", wsFreeSwitchProcessed)

	// Optional: Serve static files if needed
	// e.Static("/", "public")

	port := ":12000"
	log.Printf("🎧 FreeSWITCH Echo Server running on http://localhost%s", port)
	log.Printf("WebSocket endpoint: ws://localhost%s/ws-freeswitch", port)
	log.Printf("Using streamAudio format for playback:")
	log.Printf("  - audioDataType: raw (L16 PCM)")
	log.Printf("  - Audio will be base64 encoded")
	log.Printf("  - FreeSWITCH will save to temp and play back")
	log.Println("Ready to receive L16 audio streams from FreeSWITCH")

	e.Logger.Fatal(e.Start(port))
}
