#!/usr/bin/env python3
from flask import Flask, request, jsonify
import json
import wave
import tempfile
import os
from vosk import Model, KaldiRecognizer

app = Flask(__name__)

# Use LARGER model for better accuracy
MODEL_PATH = "models/vosk-model-en-us-0.22"  # Change this!
print(f"Loading Vosk model from {MODEL_PATH}...")
model = Model(MODEL_PATH)
print("Model loaded successfully!")

@app.route('/health', methods=['GET'])
def health():
    return jsonify({"status": "healthy", "service": "vosk-stt"})

@app.route('/transcribe', methods=['POST'])
def transcribe():
    try:
        audio_data = request.data
        sample_rate = int(request.args.get('sample_rate', 8000))

        if len(audio_data) == 0:
            return jsonify({"error": "No audio data"}), 400

        # Check minimum audio length (at least 0.5 seconds)
        min_bytes = sample_rate * 2 * 0.5  # 2 bytes per sample, 0.5 seconds
        if len(audio_data) < min_bytes:
            return jsonify({
                "success": True,
                "transcript": "",
                "sample_rate": sample_rate,
                "note": "Audio too short"
            })

        # Create temp WAV file
        with tempfile.NamedTemporaryFile(suffix='.wav', delete=False) as tmp_file:
            tmp_path = tmp_file.name

            with wave.open(tmp_path, 'wb') as wf:
                wf.setnchannels(1)
                wf.setsampwidth(2)
                wf.setframerate(sample_rate)
                wf.writeframes(audio_data)

        # Transcribe
        wf = wave.open(tmp_path, "rb")

        # Use smaller frame size for better accuracy
        rec = KaldiRecognizer(model, wf.getframerate())
        rec.SetWords(True)

        results = []
        while True:
            data = wf.readframes(2000)  # Reduced from 4000
            if len(data) == 0:
                break
            if rec.AcceptWaveform(data):
                result = json.loads(rec.Result())
                if result.get("text"):
                    results.append(result["text"])

        final_result = json.loads(rec.FinalResult())
        if final_result.get("text"):
            results.append(final_result["text"])

        transcript = " ".join(results).strip()

        # Only print non-empty transcripts
        if transcript:
            print(f"📝 Transcript ({len(audio_data)} bytes): {transcript}")

        wf.close()
        os.unlink(tmp_path)

        return jsonify({
            "success": True,
            "transcript": transcript,
            "sample_rate": sample_rate,
            "audio_length_sec": len(audio_data) / (sample_rate * 2)
        })

    except Exception as e:
        print(f"❌ Error: {e}")
        return jsonify({"error": str(e)}), 500

if __name__ == '__main__':
    app.run(host='0.0.0.0', port=5000, debug=False, threaded=True)