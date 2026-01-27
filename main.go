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

// ============================================================================
// NEW FUNCTION: ElevenLabs STT Integration via WebSocket
// ============================================================================

// ElevenLabs WebSocket message types
type ElevenLabsInputMessage struct {
	MessageType  string `json:"message_type"`
	AudioBase64  string `json:"audio_base_64,omitempty"`
	Commit       bool   `json:"commit,omitempty"`
	SampleRate   int    `json:"sample_rate,omitempty"`
	PreviousText string `json:"previous_text,omitempty"`
}

type ElevenLabsOutputMessage struct {
	MessageType string                 `json:"message_type"`
	Text        string                 `json:"text,omitempty"`
	SessionID   string                 `json:"session_id,omitempty"`
	Config      map[string]interface{} `json:"config,omitempty"`
}

// Session state for ElevenLabs connection
type ElevenLabsSession struct {
	ws          *websocket.Conn
	sessionID   string
	isConnected bool
	mu          chan struct{} // Simple mutex for session state
}

var elevenLabsSessions = make(map[string]*ElevenLabsSession)

func wsFreeSwitchElevenLabsSTT(c echo.Context) error {
	// Get ElevenLabs API key from environment
	apiKey := os.Getenv("ELEVENLABS_API_KEY")
	if apiKey == "" {
		log.Println("❌ ELEVENLABS_API_KEY environment variable not set")
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "ElevenLabs API key not configured",
		})
	}

	// Upgrade FreeSWITCH connection
	freeswitchWS, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer freeswitchWS.Close()

	sessionID := fmt.Sprintf("elevenlabs_%d", time.Now().Unix())
	log.Printf("🎙️ New FreeSWITCH -> ElevenLabs STT session: %s", sessionID)

	// Connect to ElevenLabs STT WebSocket
	elevenLabsWS, err := connectToElevenLabsSTT(apiKey, sessionID)
	if err != nil {
		log.Printf("❌ Failed to connect to ElevenLabs: %v", err)
		return err
	}
	defer elevenLabsWS.Close()

	// Store session
	session := &ElevenLabsSession{
		ws:          elevenLabsWS,
		sessionID:   sessionID,
		isConnected: true,
		mu:          make(chan struct{}, 1),
	}
	elevenLabsSessions[sessionID] = session
	defer delete(elevenLabsSessions, sessionID)

	// Start goroutine to receive transcriptions from ElevenLabs
	transcriptChan := make(chan string, 10)
	errorChan := make(chan error, 1)

	go receiveElevenLabsTranscripts(elevenLabsWS, transcriptChan, errorChan, sessionID)

	// Handle FreeSWITCH audio and send to ElevenLabs
	currentSampleRate := 8000
	messageCount := 0

	// Create a done channel
	done := make(chan struct{})

	// Handle transcripts and send back to FreeSWITCH
	go func() {
		for {
			select {
			case transcript, ok := <-transcriptChan:
				if !ok {
					return
				}
				log.Printf("📝 [%s] Transcript: %s", sessionID, transcript)

				// Send transcript back to FreeSWITCH
				response := map[string]interface{}{
					"type":       "transcript",
					"text":       transcript,
					"timestamp":  time.Now().Unix(),
					"session_id": sessionID,
					"source":     "elevenlabs",
				}
				responseJSON, _ := json.Marshal(response)
				freeswitchWS.WriteMessage(websocket.TextMessage, responseJSON)

			case err := <-errorChan:
				log.Printf("❌ [%s] ElevenLabs error: %v", sessionID, err)
				close(done)
				return

			case <-done:
				return
			}
		}
	}()

	// Main loop: Read from FreeSWITCH and send to ElevenLabs
	for {
		freeswitchWS.SetReadDeadline(time.Now().Add(60 * time.Second))
		messageType, message, err := freeswitchWS.ReadMessage()
		if err != nil {
			log.Printf("⚠️ [%s] FreeSWITCH connection closed: %v", sessionID, err)
			close(done)
			break
		}

		switch messageType {
		case websocket.TextMessage:
			// Handle metadata from FreeSWITCH
			var meta StreamMetadata
			if json.Unmarshal(message, &meta) == nil && meta.SampleRate > 0 {
				currentSampleRate = meta.SampleRate
				log.Printf("📊 [%s] Sample rate updated: %d Hz", sessionID, currentSampleRate)
			}

		case websocket.BinaryMessage:
			messageCount++

			// Send audio to ElevenLabs
			audioBase64 := base64.StdEncoding.EncodeToString(message)

			elevenLabsMsg := ElevenLabsInputMessage{
				MessageType: "input_audio_chunk",
				AudioBase64: audioBase64,
				SampleRate:  currentSampleRate,
				Commit:      false, // Auto-commit based on VAD if configured
			}

			if err := elevenLabsWS.WriteJSON(elevenLabsMsg); err != nil {
				log.Printf("❌ [%s] Failed to send audio to ElevenLabs: %v", sessionID, err)
				continue
			}

			// Echo audio back to FreeSWITCH
			// echoResp := StreamAudioResponse{
			//	Type: "streamAudio",
			//	Data: StreamAudioData{
			//		AudioDataType: "raw",
			//		SampleRate:    currentSampleRate,
			//		AudioData:     audioBase64,
			//	},
			// }
			// echoJSON, _ := json.Marshal(echoResp)
			// freeswitchWS.WriteMessage(websocket.TextMessage, echoJSON)

			if messageCount%100 == 0 {
				log.Printf("📡 [%s] Processed %d audio chunks", sessionID, messageCount)
			}
		}
	}

	log.Printf("✅ [%s] Session ended. Processed %d audio chunks", sessionID, messageCount)
	return nil
}

// Connect to ElevenLabs STT WebSocket
func connectToElevenLabsSTT(apiKey, sessionID string) (*websocket.Conn, error) {
	// ElevenLabs STT WebSocket URL
	// Use scribe_v2_realtime model for low latency
	url := "wss://api.elevenlabs.io/v1/speech-to-text/realtime?model_id=scribe_v2_realtime"

	// Add optional parameters for better transcription
	url += "&include_timestamps=false" // Set to true if you want word-level timestamps
	url += "&audio_format=pcm_16000"   // PCM 16kHz format
	url += "&commit_strategy=vad"      // Use Voice Activity Detection for auto-commit
	url += "&language_code=en"         // English language

	// Create custom header with API key
	headers := http.Header{}
	headers.Add("xi-api-key", apiKey)

	// Connect to ElevenLabs
	log.Printf("🔌 [%s] Connecting to ElevenLabs STT...", sessionID)
	conn, _, err := websocket.DefaultDialer.Dial(url, headers)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to ElevenLabs: %w", err)
	}

	log.Printf("✅ [%s] Connected to ElevenLabs STT", sessionID)
	return conn, nil
}

// Receive transcripts from ElevenLabs
func receiveElevenLabsTranscripts(ws *websocket.Conn, transcriptChan chan string, errorChan chan error, sessionID string) {
	defer close(transcriptChan)

	for {
		var msg ElevenLabsOutputMessage
		if err := ws.ReadJSON(&msg); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("ℹ️ [%s] ElevenLabs connection closed normally", sessionID)
			} else {
				log.Printf("❌ [%s] Error reading from ElevenLabs: %v", sessionID, err)
				errorChan <- err
			}
			return
		}

		switch msg.MessageType {
		case "session_started":
			log.Printf("✅ [%s] ElevenLabs session started: %s", sessionID, msg.SessionID)

		case "partial_transcript":
			// Partial transcripts (real-time, may change)
			log.Printf("🔄 [%s] Partial: %s", sessionID, msg.Text)
			// Optionally send partial transcripts
			// transcriptChan <- msg.Text

		case "committed_transcript":
			// Final committed transcripts
			if msg.Text != "" {
				transcriptChan <- msg.Text
			}

		case "committed_transcript_with_timestamps":
			// If timestamps are enabled
			if msg.Text != "" {
				transcriptChan <- msg.Text
			}

		default:
			// Handle error messages
			if msg.MessageType != "" {
				log.Printf("⚠️ [%s] ElevenLabs message type: %s", sessionID, msg.MessageType)
			}
		}
	}
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

	// NEW ROUTE: ElevenLabs STT
	e.GET("/ws-elevenlabs-stt", wsFreeSwitchElevenLabsSTT)

	port := ":12000"
	log.Printf("🎧 FreeSWITCH Echo Server running on http://localhost%s", port)
	log.Printf("WebSocket endpoints:")
	log.Printf("  - ws://localhost%s/ws-freeswitch (Echo only)", port)
	log.Printf("  - ws://localhost%s/ws-processed (Basic processing)", port)
	log.Printf("  - ws://localhost%s/ws-processed-vosk-sst (Vosk STT)", port)
	log.Printf("  - ws://localhost%s/ws-elevenlabs-stt (ElevenLabs STT) ⭐ NEW", port)
	log.Println("Ready to receive L16 audio streams from FreeSWITCH")

	e.Logger.Fatal(e.Start(port))
}
