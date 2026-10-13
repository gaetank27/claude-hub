// Reconnecting WebSocket client for a session stream.
//
// On every (re)connection the backend replays the full journal before live
// events, so consumers MUST clear their transcript on `onReset` (fired on open)
// to avoid duplicating history after a reconnect — e.g. when the backend
// restarts during self-evolution.

export type StreamHandlers = {
  onEvent: (event: any) => void;
  onReset: () => void;
  onStatus: (status: "connecting" | "open" | "closed") => void;
};

// A pasted image: raw base64 (no data: URI prefix) plus its media type.
export type ImageInput = { mediaType: string; data: string };

// An inline document (e.g. a PDF): same shape as an image, sent as an Anthropic
// "document" content block so the model reads it natively. Larger files go to
// disk via the attachments endpoint instead and are referenced by path.
export type DocumentInput = { mediaType: string; data: string };

// What the composer sends: a text prompt, any inline images, and any inline
// documents (PDFs). The whole payload is JSON-serialized into one WS frame, so
// the backend's buildInput picks the documents field up with no extra wiring.
export type SendPayload = { text: string; images?: ImageInput[]; documents?: DocumentInput[]; voice?: boolean };

export class SessionSocket {
  private ws: WebSocket | null = null;
  private closed = false;
  private backoff = 500;
  // Consecutive connections that closed before ever opening. The auth middleware
  // rejects the WS upgrade with 401 once the session expires, which surfaces as a
  // close-before-open; after a few in a row, bounce to the login flow instead of
  // reconnecting forever.
  private failsBeforeOpen = 0;

  constructor(private sessionId: string, private h: StreamHandlers) {
    this.connect();
  }

  private connect() {
    if (this.closed) return;
    this.h.onStatus("connecting");
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(
      `${proto}://${location.host}/api/sessions/${this.sessionId}/stream`,
    );
    this.ws = ws;
    let opened = false;

    ws.onopen = () => {
      opened = true;
      this.failsBeforeOpen = 0;
      this.backoff = 500;
      this.h.onReset();
      this.h.onStatus("open");
    };
    ws.onmessage = (ev) => {
      for (const line of String(ev.data).split("\n")) {
        if (!line.trim()) continue;
        try {
          this.h.onEvent(JSON.parse(line));
        } catch {
          /* tolerate malformed/partial line */
        }
      }
    };
    ws.onclose = () => {
      this.h.onStatus("closed");
      if (this.closed) return;
      if (!opened && ++this.failsBeforeOpen >= 3) {
        window.location.href = "/";
        return;
      }
      setTimeout(() => this.connect(), this.backoff);
      this.backoff = Math.min(this.backoff * 2, 5000);
    };
    ws.onerror = () => ws.close();
  }

  // Accepts a plain prompt (e.g. answering a question) or a full payload with
  // images. Always sent as the JSON envelope the backend's buildInput expects.
  send(payload: SendPayload | string) {
    const p: SendPayload = typeof payload === "string" ? { text: payload } : payload;
    this.ws?.send(JSON.stringify(p));
  }

  close() {
    this.closed = true;
    this.ws?.close();
  }
}
