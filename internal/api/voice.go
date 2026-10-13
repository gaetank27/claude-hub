package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const maxVoiceAudioBytes = 16 * 1024 * 1024

// voiceStatus reports whether local voice (STT+TTS) is available.
func (s *Server) voiceStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"available": s.voice != nil && s.voice.Available()})
}

// voiceSTT transcribes the raw audio body (a browser MediaRecorder blob).
func (s *Server) voiceSTT(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		http.Error(w, "voice unavailable", http.StatusServiceUnavailable)
		return
	}
	audio, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxVoiceAudioBytes))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		lang = "fr"
	}
	text, err := s.voice.Transcribe(r.Context(), audio, lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"text": text})
}

// voiceTTS synthesizes {"text": ...} into a WAV.
func (s *Server) voiceTTS(w http.ResponseWriter, r *http.Request) {
	if s.voice == nil {
		http.Error(w, "voice unavailable", http.StatusServiceUnavailable)
		return
	}
	var in struct{ Text string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	wav, err := s.voice.Synthesize(r.Context(), in.Text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Write(wav)
}
