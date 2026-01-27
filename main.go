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

	"github.com/joho/godotenv"

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

// ElevenLabs Conversational AI message types
type ConvAIClientMessage struct {
	Type                       string                      `json:"type,omitempty"`
	UserAudioChunk             string                      `json:"user_audio_chunk,omitempty"`
	EventID                    int64                       `json:"event_id,omitempty"`
	ConversationConfigOverride *ConversationConfigOverride `json:"conversation_config_override,omitempty"`
	CustomLLMExtraBody         map[string]interface{}      `json:"custom_llm_extra_body,omitempty"`
	DynamicVariables           map[string]string           `json:"dynamic_variables,omitempty"`
	ToolCallID                 string                      `json:"tool_call_id,omitempty"`
	Result                     string                      `json:"result,omitempty"`
	IsError                    bool                        `json:"is_error,omitempty"`
	Text                       string                      `json:"text,omitempty"`
}

type ConversationConfigOverride struct {
	Agent *AgentConfig `json:"agent,omitempty"`
	TTS   *TTSConfig   `json:"tts,omitempty"`
}

type AgentConfig struct {
	Prompt       *PromptConfig `json:"prompt,omitempty"`
	FirstMessage string        `json:"first_message,omitempty"`
	Language     string        `json:"language,omitempty"`
}

type PromptConfig struct {
	Prompt string `json:"prompt,omitempty"`
}

type TTSConfig struct {
	VoiceID string `json:"voice_id,omitempty"`
}

type ConvAIServerMessage struct {
	Type                           string                       `json:"type"`
	ConversationInitiationMetadata *ConversationInitMetadata    `json:"conversation_initiation_metadata_event,omitempty"`
	UserTranscriptionEvent         *UserTranscriptionEvent      `json:"user_transcription_event,omitempty"`
	AgentResponseEvent             *AgentResponseEvent          `json:"agent_response_event,omitempty"`
	AudioEvent                     *AudioEvent                  `json:"audio_event,omitempty"`
	PingEvent                      *PingEvent                   `json:"ping_event,omitempty"`
	InterruptionEvent              *InterruptionEvent           `json:"interruption_event,omitempty"`
	ClientToolCall                 *ClientToolCall              `json:"client_tool_call,omitempty"`
	TentativeAgentResponseEvent    *TentativeAgentResponseEvent `json:"tentative_agent_response_internal_event,omitempty"`
}

type ConversationInitMetadata struct {
	ConversationID         string `json:"conversation_id"`
	AgentOutputAudioFormat string `json:"agent_output_audio_format"`
	UserInputAudioFormat   string `json:"user_input_audio_format"`
}

type UserTranscriptionEvent struct {
	UserTranscript string `json:"user_transcript"`
}

type AgentResponseEvent struct {
	AgentResponse string `json:"agent_response"`
}

type AudioEvent struct {
	AudioBase64 string `json:"audio_base_64"`
	EventID     int    `json:"event_id"`
}

type PingEvent struct {
	EventID int `json:"event_id"`
	PingMs  int `json:"ping_ms"`
}

type InterruptionEvent struct {
	EventID int `json:"event_id"`
}

type ClientToolCall struct {
	ToolName   string                 `json:"tool_name"`
	ToolCallID string                 `json:"tool_call_id"`
	Parameters map[string]interface{} `json:"parameters"`
}

type TentativeAgentResponseEvent struct {
	TentativeAgentResponse string `json:"tentative_agent_response"`
}

func wsFreeSwitchConversationalAI(c echo.Context) error {
	// Get credentials from environment
	apiKey := os.Getenv("ELEVENLABS_API_KEY")
	agentID := os.Getenv("ELEVENLABS_AGENT_ID")

	if apiKey == "" {
		log.Println("❌ ELEVENLABS_API_KEY environment variable not set")
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "ElevenLabs API key not configured",
		})
	}

	if agentID == "" {
		log.Println("❌ ELEVENLABS_AGENT_ID environment variable not set")
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "ElevenLabs Agent ID not configured",
		})
	}

	// Upgrade FreeSWITCH connection
	freeswitchWS, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	defer freeswitchWS.Close()

	sessionID := fmt.Sprintf("convai_%d", time.Now().Unix())
	log.Printf("🤖 New FreeSWITCH -> ElevenLabs Conversational AI session: %s", sessionID)

	// Connect to ElevenLabs Conversational AI WebSocket
	convaiWS, err := connectToConversationalAI(apiKey, agentID, sessionID)
	if err != nil {
		log.Printf("❌ Failed to connect to ElevenLabs Conversational AI: %v", err)
		return err
	}
	defer convaiWS.Close()

	// Send initial conversation configuration (optional)
	initMsg := ConvAIClientMessage{
		Type: "conversation_initiation_client_data",
		ConversationConfigOverride: &ConversationConfigOverride{
			Agent: &AgentConfig{
				Prompt: &PromptConfig{
					Prompt: "You are a helpful and friendly customer support agent. Be concise and clear in your responses.",
				},
				FirstMessage: "Hello! How can I assist you today?",
				Language:     "en",
			},
		},
		CustomLLMExtraBody: map[string]interface{}{
			"temperature": 0.7,
			"max_tokens":  200,
		},
	}

	if err := convaiWS.WriteJSON(initMsg); err != nil {
		log.Printf("❌ Failed to send initialization message: %v", err)
	}

	// Channels for communication
	agentAudioChan := make(chan []byte, 100)
	errorChan := make(chan error, 1)
	done := make(chan struct{})

	// Goroutine to receive from ElevenLabs and send to FreeSWITCH
	go receiveConversationalAI(convaiWS, freeswitchWS, agentAudioChan, errorChan, sessionID)

	// Goroutine to send agent audio to FreeSWITCH
	go func() {
		currentSampleRate := 16000 // ElevenLabs typically uses 16kHz
		for {
			select {
			case audioData, ok := <-agentAudioChan:
				if !ok {
					return
				}

				// Send agent's TTS audio back to FreeSWITCH
				audioBase64 := base64.StdEncoding.EncodeToString(audioData)
				resp := StreamAudioResponse{
					Type: "streamAudio",
					Data: StreamAudioData{
						AudioDataType: "raw",
						SampleRate:    currentSampleRate,
						AudioData:     audioBase64,
					},
				}

				respJSON, _ := json.Marshal(resp)
				if err := freeswitchWS.WriteMessage(websocket.TextMessage, respJSON); err != nil {
					log.Printf("⚠️ [%s] Failed to send audio to FreeSWITCH: %v", sessionID, err)
				}

			case <-done:
				return
			}
		}
	}()

	// Main loop: Read from FreeSWITCH and send to ElevenLabs
	currentSampleRate := 8000
	messageCount := 0

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
				log.Printf("📊 [%s] Sample rate: %d Hz", sessionID, currentSampleRate)
			}

		case websocket.BinaryMessage:
			messageCount++

			// Send user audio to ElevenLabs Conversational AI
			audioBase64 := base64.StdEncoding.EncodeToString(message)

			audioMsg := ConvAIClientMessage{
				UserAudioChunk: audioBase64,
			}

			if err := convaiWS.WriteJSON(audioMsg); err != nil {
				log.Printf("❌ [%s] Failed to send audio to ConvAI: %v", sessionID, err)
				continue
			}

			if messageCount%100 == 0 {
				log.Printf("📡 [%s] Processed %d audio chunks", sessionID, messageCount)
			}
		}
	}

	log.Printf("✅ [%s] Session ended. Processed %d audio chunks", sessionID, messageCount)
	return nil
}

// Connect to ElevenLabs Conversational AI WebSocket
func connectToConversationalAI(apiKey, agentID, sessionID string) (*websocket.Conn, error) {
	url := fmt.Sprintf("wss://api.elevenlabs.io/v1/convai/conversation?agent_id=%s", agentID)

	headers := http.Header{}
	headers.Add("xi-api-key", apiKey)

	log.Printf("🔌 [%s] Connecting to ElevenLabs Conversational AI...", sessionID)
	conn, resp, err := websocket.DefaultDialer.Dial(url, headers)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("failed to connect: %v, response: %s", err, string(body))
		}
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	log.Printf("✅ [%s] Connected to ElevenLabs Conversational AI", sessionID)
	return conn, nil
}

// Receive messages from ElevenLabs Conversational AI
func receiveConversationalAI(convaiWS, freeswitchWS *websocket.Conn, agentAudioChan chan []byte, errorChan chan error, sessionID string) {
	defer close(agentAudioChan)

	for {
		var msg ConvAIServerMessage
		if err := convaiWS.ReadJSON(&msg); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("ℹ️ [%s] ConvAI connection closed normally", sessionID)
			} else {
				log.Printf("❌ [%s] Error reading from ConvAI: %v", sessionID, err)
				errorChan <- err
			}
			return
		}

		switch msg.Type {
		case "conversation_initiation_metadata":
			if msg.ConversationInitiationMetadata != nil {
				log.Printf("✅ [%s] Conversation started: %s", sessionID, msg.ConversationInitiationMetadata.ConversationID)
				log.Printf("   Audio format: %s -> %s",
					msg.ConversationInitiationMetadata.UserInputAudioFormat,
					msg.ConversationInitiationMetadata.AgentOutputAudioFormat)
			}

		case "user_transcript":
			if msg.UserTranscriptionEvent != nil {
				log.Printf("👤 [%s] User said: %s", sessionID, msg.UserTranscriptionEvent.UserTranscript)
			}

		case "agent_response":
			if msg.AgentResponseEvent != nil {
				log.Printf("🤖 [%s] Agent response: %s", sessionID, msg.AgentResponseEvent.AgentResponse)
			}

		case "internal_tentative_agent_response":
			if msg.TentativeAgentResponseEvent != nil {
				log.Printf("💭 [%s] Agent thinking: %s", sessionID, msg.TentativeAgentResponseEvent.TentativeAgentResponse)
			}

		case "audio":
			if msg.AudioEvent != nil {
				// Decode base64 audio from agent
				audioData, err := base64.StdEncoding.DecodeString(msg.AudioEvent.AudioBase64)
				if err != nil {
					log.Printf("❌ [%s] Failed to decode audio: %v", sessionID, err)
					continue
				}

				// Send to channel for FreeSWITCH playback
				agentAudioChan <- audioData
			}

		case "interruption":
			if msg.InterruptionEvent != nil {
				log.Printf("⚠️ [%s] User interrupted agent", sessionID)
			}

		case "ping":
			if msg.PingEvent != nil {
				// Respond with pong
				pongMsg := ConvAIClientMessage{
					Type:    "pong",
					EventID: int64(msg.PingEvent.EventID),
				}
				convaiWS.WriteJSON(pongMsg)
			}

		case "client_tool_call":
			if msg.ClientToolCall != nil {
				log.Printf("🔧 [%s] Tool call: %s (ID: %s)", sessionID, msg.ClientToolCall.ToolName, msg.ClientToolCall.ToolCallID)
				// You can implement tool calling here if needed
			}

		default:
			// Log unknown message types
			if msg.Type != "" {
				log.Printf("🔍 [%s] Unknown message type: %s", sessionID, msg.Type)
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

	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ No .env file found, using system environment variables")
	}

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

	e.GET("/ws-conversational-ai", wsFreeSwitchConversationalAI)

	port := ":12000"
	log.Printf("🎧 FreeSWITCH Echo Server running on http://localhost%s", port)
	log.Printf("WebSocket endpoints:")
	log.Printf("  - ws://localhost%s/ws-freeswitch (Echo only)", port)
	log.Printf("  - ws://localhost%s/ws-processed (Basic processing)", port)
	log.Printf("  - ws://localhost%s/ws-processed-vosk-sst (Vosk STT)", port)
	log.Printf("  - ws://localhost%s/ws-elevenlabs-stt (ElevenLabs STT) TTS only", port)
	log.Printf("  - ws://localhost%s/ws-conversational-ai (ElevenLabs Full AI Agent)", port)
	log.Println("Ready to receive L16 audio streams from FreeSWITCH")

	e.Logger.Fatal(e.Start(port))
}
