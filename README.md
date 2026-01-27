# FS integration with Custom Mod Audio Stream








# Vosk

```
cd /root/vosk-stt
python3 -m .venv venv

source venv/bin/activate

mkdir -p models
cd /root/vosk-stt/models

pip install vosk
pip install soundfile

// Download small English model (~40MB, fast)
wget https://alphacephei.com/vosk/models/vosk-model-small-en-us-0.15.zip

unzip vosk-model-small-en-us-0.15.zip


// Download larger model (~1.8GB, much better accuracy)
// wget https://alphacephei.com/vosk/models/vosk-model-en-us-0.22.zip
// unzip vosk-model-en-us-0.22.zip
```

Results:
The 1.8 GB model is too big and crashed a lot.
The 40 MB model is fast, but not accurate, failed to pick most words