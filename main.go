package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
	ws.SetReadDeadline(time.Now().Add(60 * time.Second))

	// Generate unique session ID immediately
	recordingDir := "recordings"
	os.MkdirAll(recordingDir, 0755)

	filename := "session_" + time.Now().Format("20060102_150405") + "_audio.raw"
	fullPath := filepath.Join(recordingDir, filename)

	fullFile, err := os.Create(fullPath)
	if err != nil {
		log.Printf("Warning: cannot create %s: %v", fullPath, err)
	} else {
		log.Printf("Recording audio to: %s", fullPath)
		defer fullFile.Close()
	}

	currentSampleRate := 8000
	messageCount := 0

	for {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		messageType, message, err := ws.ReadMessage()
		if err != nil {
			break
		}

		switch messageType {
		case websocket.TextMessage:
			// Optional: parse metadata to update sample rate
			var meta StreamMetadata
			if json.Unmarshal(message, &meta) == nil && meta.SampleRate > 0 {
				currentSampleRate = meta.SampleRate
			}

		case websocket.BinaryMessage:
			messageCount++

			if fullFile != nil {
				fullFile.Write(message) // ignore error for speed
			}

			// Echo back
			audioBase64 := base64.StdEncoding.EncodeToString(message)
			resp := StreamAudioResponse{
				Type: "streamAudio",
				Data: StreamAudioData{
					AudioDataType: "raw",
					SampleRate:    currentSampleRate,
					AudioData:     audioBase64,
				},
			}
			if jsonBytes, _ := json.Marshal(resp); len(jsonBytes) > 0 {
				ws.WriteMessage(websocket.TextMessage, jsonBytes)
			}
		}
	}

	log.Printf("CloseOperation. Saved %d chunks to %s", messageCount, fullPath)
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

type VoskResponse struct {
	Success    bool   `json:"success"`
	Transcript string `json:"transcript"`
	SampleRate int    `json:"sample_rate"`
}

// Audio buffer for STT processing
type AudioBuffer struct {
	data       []byte
	sampleRate int
	lastSent   time.Time
}

var audioBuffers = make(map[string]*AudioBuffer)

func wsFreeSwitchProcessedVoskSST(c echo.Context) error {
	ws, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer ws.Close()

	log.Printf("New FreeSWITCH processed audio connection from %s", c.Request().RemoteAddr)

	currentSampleRate := 8000
	sessionID := fmt.Sprintf("session_%d", time.Now().Unix())

	// Initialize audio buffer for this session
	audioBuffers[sessionID] = &AudioBuffer{
		data:       make([]byte, 0),
		sampleRate: currentSampleRate,
		lastSent:   time.Now(),
	}
	defer delete(audioBuffers, sessionID)

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
					audioBuffers[sessionID].sampleRate = currentSampleRate
				}
				log.Printf("Metadata received - SampleRate: %d", currentSampleRate)
			}

		case websocket.BinaryMessage:
			// Add to buffer for STT processing
			audioBuffers[sessionID].data = append(audioBuffers[sessionID].data, message...)

			// Process every 3 seconds of audio
			if time.Since(audioBuffers[sessionID].lastSent) >= 3*time.Second {
				go processAudioChunk(sessionID, ws)
				audioBuffers[sessionID].lastSent = time.Now()
			}

			// Original echo functionality
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

// NEW FUNCTION: Process audio chunk with Vosk STT
func processAudioChunk(sessionID string, ws *websocket.Conn) {
	buffer, exists := audioBuffers[sessionID]
	if !exists || len(buffer.data) == 0 {
		return
	}

	// Copy and clear buffer
	audioData := make([]byte, len(buffer.data))
	copy(audioData, buffer.data)
	buffer.data = buffer.data[:0]

	log.Printf("📤 Sending %d bytes to Vosk STT (sample rate: %d)", len(audioData), buffer.sampleRate)

	// Send to Vosk
	transcript, err := sendToVoskSTT(audioData, buffer.sampleRate)
	if err != nil {
		log.Printf("❌ STT Error: %v", err)
		return
	}

	if transcript != "" {
		log.Printf("📝 Transcript: %s", transcript)

		// Optional: Send transcript back to FreeSWITCH as a JSON message
		response := map[string]interface{}{
			"type":       "transcript",
			"text":       transcript,
			"timestamp":  time.Now().Unix(),
			"session_id": sessionID,
		}
		responseJSON, _ := json.Marshal(response)
		ws.WriteMessage(websocket.TextMessage, responseJSON)
	}
}

// NEW FUNCTION: Send audio to Vosk HTTP server
func sendToVoskSTT(audioData []byte, sampleRate int) (string, error) {
	url := fmt.Sprintf("http://localhost:5000/transcribe?sample_rate=%d", sampleRate)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(audioData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("vosk returned status %d: %s", resp.StatusCode, string(body))
	}

	var result VoskResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if !result.Success {
		return "", fmt.Errorf("vosk transcription failed")
	}

	return result.Transcript, nil
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
	e.GET("/ws-processed-vosk-sst", wsFreeSwitchProcessedVoskSST)

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
