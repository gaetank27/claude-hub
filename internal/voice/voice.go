// Package voice provides fully local speech-to-text (faster-whisper) and
// text-to-speech (Piper) for the Hub's voice mode. Both models are served by a
// single long-lived Python sidecar bound to loopback, started lazily on first
// use and kept warm so each request pays inference time only, not model load.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// DefaultVoice is the Piper voice used when none is configured. The model
// files live in <dataDir>/piper_voices/<voice>.onnx(.json).
const DefaultVoice = "fr_FR-siwis-medium"

// serverScript is written to <dataDir>/voice_server.py on startup so updates
// are always deployed (same pattern as the WhatsApp transcribe helper).
const serverScript = `#!/usr/bin/env python3
"""Local STT (faster-whisper) + TTS (Piper) sidecar. Usage: voice_server.py <dataDir> <port>"""
import io, json, os, sys, tempfile, threading, wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

data_dir, port = sys.argv[1], int(sys.argv[2])
lock = threading.Lock()
_whisper = None
_voices = {}

def whisper():
    global _whisper
    if _whisper is None:
        from faster_whisper import WhisperModel
        _whisper = WhisperModel("small", device="cpu", compute_type="int8",
                                download_root=os.path.join(data_dir, "whisper_model"))
    return _whisper

def voice(name):
    if name not in _voices:
        from piper import PiperVoice
        _voices[name] = PiperVoice.load(os.path.join(data_dir, "piper_voices", name + ".onnx"))
    return _voices[name]

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def _send(self, code, body, ctype):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        try:
            if self.path.startswith("/stt"):
                lang = self.headers.get("X-Lang") or "fr"
                with tempfile.NamedTemporaryFile(suffix=".audio") as f:
                    f.write(body); f.flush()
                    with lock:
                        segs, _ = whisper().transcribe(f.name, language=lang, beam_size=1,
                                                       vad_filter=True, condition_on_previous_text=False)
                        text = " ".join(s.text.strip() for s in segs).strip()
                self._send(200, json.dumps({"text": text}).encode(), "application/json")
            elif self.path.startswith("/tts"):
                req = json.loads(body)
                buf = io.BytesIO()
                with lock:
                    v = voice(req.get("voice") or "fr_FR-siwis-medium")
                    with wave.open(buf, "wb") as w:
                        v.synthesize_wav(req["text"], w)
                self._send(200, buf.getvalue(), "audio/wav")
            else:
                self._send(404, b"not found", "text/plain")
        except Exception as e:
            self._send(500, str(e).encode(), "text/plain")

ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
`

// Service supervises the sidecar and proxies STT/TTS requests to it.
type Service struct {
	dataDir string
	voice   string

	mu   sync.Mutex
	cmd  *exec.Cmd
	port int
}

// New writes the sidecar script and returns a Service. The process itself is
// started lazily by the first request.
func New(dataDir string) *Service {
	s := &Service{dataDir: dataDir, voice: DefaultVoice}
	if err := os.WriteFile(filepath.Join(dataDir, "voice_server.py"), []byte(serverScript), 0o755); err != nil {
		stdlog.Printf("[VOICE WARN] write voice_server.py: %v", err)
	}
	return s
}

// Available reports whether the Piper voice model is installed.
func (s *Service) Available() bool {
	_, err := os.Stat(filepath.Join(s.dataDir, "piper_voices", s.voice+".onnx"))
	return err == nil
}

// ensure starts the sidecar if it is not running and returns its port.
func (s *Service) ensure() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.ProcessState == nil {
		return s.port, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command("python3", filepath.Join(s.dataDir, "voice_server.py"), s.dataDir, fmt.Sprint(port))
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	s.cmd, s.port = cmd, port
	go func() {
		cmd.Wait()
		s.mu.Lock()
		if s.cmd == cmd {
			s.cmd = nil
		}
		s.mu.Unlock()
	}()
	// Wait until the socket accepts connections.
	for i := 0; i < 50; i++ {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			c.Close()
			return port, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, fmt.Errorf("voice sidecar did not start")
}

func (s *Service) post(ctx context.Context, path string, body []byte, hdr map[string]string) ([]byte, error) {
	port, err := s.ensure()
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://127.0.0.1:%d%s", port, path), bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("voice: %s", bytes.TrimSpace(out))
	}
	return out, nil
}

// Transcribe converts recorded audio (any format PyAV decodes) to text.
func (s *Service) Transcribe(ctx context.Context, audio []byte, lang string) (string, error) {
	out, err := s.post(ctx, "/stt", audio, map[string]string{"X-Lang": lang})
	if err != nil {
		return "", err
	}
	var r struct{ Text string }
	if err := json.Unmarshal(out, &r); err != nil {
		return "", err
	}
	return r.Text, nil
}

// Synthesize renders text to a WAV file.
func (s *Service) Synthesize(ctx context.Context, text string) ([]byte, error) {
	b, _ := json.Marshal(map[string]string{"text": text, "voice": s.voice})
	return s.post(ctx, "/tts", b, nil)
}

// Close stops the sidecar.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
}
