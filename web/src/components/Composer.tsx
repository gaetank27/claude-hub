import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ImageInput, DocumentInput, SendPayload } from "../lib/ws";
import type { AttachmentResult } from "../lib/api";
import { Button } from "./ui";

// Inline-attachment limits. The runner reads one stream-json line at a time with
// an 8MB scanner buffer (internal/runner/socket.go); base64 inflates bytes by
// ~33%, and every inline image/PDF plus the text share that single line. Cap raw
// size per item and the combined base64 well under 8MB so a frame is never
// silently truncated. Files that don't fit (or aren't inlineable) go to disk via
// the attachments endpoint instead.
const ACCEPTED_IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];
const MAX_IMAGE_BYTES = 4 * 1024 * 1024; // per image, before base64
const MAX_INLINE_PDF_BYTES = 4 * 1024 * 1024; // per PDF, before base64; bigger PDFs fall back to disk
const MAX_TOTAL_BASE64 = 7 * 1024 * 1024; // combined inline (images + PDFs), after base64

type Attachment = ImageInput & { id: number; preview: string };
type DocAttachment = DocumentInput & { id: number; name: string; size: number };
type FileAttachment = { id: number; name: string; size: number; uploading: boolean; path: string };

// readBase64 turns a file into a base64 payload (+ a data: URL preview),
// stripping the data: URI prefix the backend does not want.
function readBase64(file: File): Promise<{ mediaType: string; data: string; preview: string }> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error("read failed"));
    reader.onload = () => {
      const url = String(reader.result);
      const comma = url.indexOf(",");
      resolve({ mediaType: file.type, data: url.slice(comma + 1), preview: url });
    };
    reader.readAsDataURL(file);
  });
}

function fmtSize(n: number): string {
  if (n < 1024) return `${n} o`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} Ko`;
  return `${(n / 1024 / 1024).toFixed(1)} Mo`;
}

// Browser speech-to-text, where available (Chrome/Edge/Safari; absent on Firefox).
const SpeechRecognition: any =
  typeof window !== "undefined"
    ? (window as any).SpeechRecognition || (window as any).webkitSpeechRecognition
    : undefined;

// The API only works in a secure context (HTTPS, or localhost). On the plain-HTTP
// LAN deployment the object exists but start() is rejected, so hide the mic until
// the hub is served over HTTPS (planned with OIDC auth). Re-enables itself then.
const voiceAvailable =
  typeof window !== "undefined" && !!SpeechRecognition && window.isSecureContext;

// Touch devices have no physical keyboard, so Enter must insert a newline (the
// send button submits). On desktop, Enter sends and Shift+Enter inserts a newline.
const isTouch =
  typeof window !== "undefined" && window.matchMedia?.("(pointer: coarse)").matches;

// Prompt input.
export function Composer({
  disabled,
  onSend,
  onUpload,
}: {
  disabled: boolean;
  onSend: (payload: SendPayload) => void;
  // Uploads files to the session's project working dir; returns their saved
  // paths. Provided by the parent (which holds the session id). When absent,
  // non-inlineable files cannot be attached.
  onUpload?: (files: File[]) => Promise<AttachmentResult[]>;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState("");
  const [images, setImages] = useState<Attachment[]>([]);
  const [docs, setDocs] = useState<DocAttachment[]>([]);
  const [files, setFiles] = useState<FileAttachment[]>([]);
  const [error, setError] = useState("");
  const [listening, setListening] = useState(false);
  const recognitionRef = useRef<any>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const nextId = useRef(0);

  // Stop any in-flight dictation when the composer unmounts (session switch).
  useEffect(() => () => recognitionRef.current?.stop(), []);

  // Auto-grow the textarea with its content so the user can read what they type
  // (critical on mobile, where a fixed 2-row box scrolls internally and hides the
  // start of the message). Grows up to a max (CSS max-h) then becomes scrollable.
  // Runs on every `text` change, covering programmatic updates (dictation, clear
  // on submit) as well as typing.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, [text]);

  // uploadDisk sends files to the project working dir and tracks each as a chip
  // (spinner while uploading, then resolved to its saved path).
  async function uploadDisk(toUpload: File[]) {
    if (!onUpload) {
      setError(t("session.fileAttachUnavailable"));
      return;
    }
    const placeholders: FileAttachment[] = toUpload.map((f) => ({
      id: nextId.current++,
      name: f.name,
      size: f.size,
      uploading: true,
      path: "",
    }));
    setFiles((prev) => [...prev, ...placeholders]);
    try {
      const results = await onUpload(toUpload);
      setFiles((prev) =>
        prev.map((p) => {
          const i = placeholders.findIndex((ph) => ph.id === p.id);
          if (i < 0 || !results[i]) return p;
          return { ...p, uploading: false, path: results[i].path };
        }),
      );
    } catch {
      setError(t("session.uploadFailed"));
      setFiles((prev) => prev.filter((p) => !placeholders.some((ph) => ph.id === p.id)));
    }
  }

  // addFiles routes each attachment by type: images and small PDFs are read
  // inline as base64 content blocks (shared 8MB-frame budget); a PDF that won't
  // fit, and every other file type (zip, tgz, …), goes to disk instead.
  async function addFiles(incoming: File[]) {
    if (incoming.length === 0) return;
    setError("");
    // Budget is computed locally (not via stale state) as we read each item.
    let budget =
      images.reduce((n, a) => n + a.data.length, 0) +
      docs.reduce((n, a) => n + a.data.length, 0);
    const newImages: Attachment[] = [];
    const newDocs: DocAttachment[] = [];
    const toUpload: File[] = [];

    for (const f of incoming) {
      if (f.type.startsWith("image/")) {
        if (!ACCEPTED_IMAGE_TYPES.includes(f.type)) {
          setError(t("session.unsupportedFormat", { type: f.type || t("session.unknownType") }));
          continue;
        }
        if (f.size > MAX_IMAGE_BYTES) {
          setError(t("session.imageTooLarge", { max: MAX_IMAGE_BYTES / 1024 / 1024 }));
          continue;
        }
        try {
          const { mediaType, data, preview } = await readBase64(f);
          if (budget + data.length > MAX_TOTAL_BASE64) {
            setError(t("session.tooManyInlineAttachments"));
            continue;
          }
          budget += data.length;
          newImages.push({ id: nextId.current++, mediaType, data, preview });
        } catch {
          setError(t("session.imageReadFailed"));
        }
      } else if (f.type === "application/pdf") {
        // PDFs go inline as native document blocks when they fit the frame
        // budget; otherwise fall back to disk so the user is never blocked.
        if (f.size > MAX_INLINE_PDF_BYTES) {
          toUpload.push(f);
          continue;
        }
        try {
          const { mediaType, data } = await readBase64(f);
          if (budget + data.length > MAX_TOTAL_BASE64) {
            toUpload.push(f);
            continue;
          }
          budget += data.length;
          newDocs.push({ id: nextId.current++, mediaType, data, name: f.name, size: f.size });
        } catch {
          toUpload.push(f);
        }
      } else {
        toUpload.push(f);
      }
    }

    if (newImages.length) setImages((prev) => [...prev, ...newImages]);
    if (newDocs.length) setDocs((prev) => [...prev, ...newDocs]);
    if (toUpload.length) await uploadDisk(toUpload);
  }

  function removeImage(id: number) {
    setImages((prev) => prev.filter((a) => a.id !== id));
  }
  function removeDoc(id: number) {
    setDocs((prev) => prev.filter((a) => a.id !== id));
  }
  function removeFile(id: number) {
    setFiles((prev) => prev.filter((a) => a.id !== id));
  }

  const uploading = files.some((f) => f.uploading);
  const empty =
    !text.trim() && images.length === 0 && docs.length === 0 && files.length === 0;

  function submit() {
    if (disabled || uploading || empty) return;
    const trimmed = text.trim();
    const uploaded = files.filter((f) => f.path);
    let body = trimmed;
    if (uploaded.length > 0) {
      const list = uploaded.map((f) => f.path).join(", ");
      body =
        (trimmed ? trimmed + "\n\n" : "") +
        t("session.attachedFilesInProject", { list });
    }
    onSend({
      text: body,
      images: images.map(({ mediaType, data }) => ({ mediaType, data })),
      documents: docs.map(({ mediaType, data }) => ({ mediaType, data })),
    });
    setText("");
    setImages([]);
    setDocs([]);
    setFiles([]);
    setError("");
  }

  function toggleMic() {
    if (!SpeechRecognition) return;
    if (listening) {
      recognitionRef.current?.stop();
      return;
    }
    const rec = new SpeechRecognition();
    rec.lang = "fr-FR";
    rec.interimResults = false;
    rec.continuous = false;
    rec.onresult = (e: any) => {
      const transcript = Array.from(e.results)
        .map((r: any) => r[0].transcript)
        .join(" ");
      setText((prev) => (prev ? prev + " " : "") + transcript);
    };
    rec.onend = () => setListening(false);
    rec.onerror = () => setListening(false);
    recognitionRef.current = rec;
    setListening(true);
    rec.start();
  }

  const chip =
    "flex items-center gap-1.5 rounded-control border border-border bg-bg px-2 py-1 text-xs text-muted";

  return (
    <div className="flex flex-col gap-2">
      {error && <div className="text-danger text-xs">{error}</div>}
      {(images.length > 0 || docs.length > 0 || files.length > 0) && (
        <div className="flex flex-wrap gap-2">
          {images.map((img) => (
            <div key={`i${img.id}`} className="relative">
              <img
                src={img.preview}
                alt={t("session.imageAlt")}
                className="h-16 w-16 object-cover rounded-control border border-border"
              />
              <button
                onClick={() => removeImage(img.id)}
                aria-label={t("session.removeImage")}
                className="absolute -top-1.5 -right-1.5 w-5 h-5 rounded-pill bg-bg border border-border text-xs leading-none text-muted hover:text-danger"
              >
                ×
              </button>
            </div>
          ))}
          {docs.map((d) => (
            <div key={`d${d.id}`} className={chip}>
              <span>📄</span>
              <span className="max-w-[12rem] truncate" title={d.name}>
                {d.name}
              </span>
              <span className="opacity-60">{fmtSize(d.size)}</span>
              <button
                onClick={() => removeDoc(d.id)}
                aria-label={t("session.removeDocument")}
                className="leading-none hover:text-danger"
              >
                ×
              </button>
            </div>
          ))}
          {files.map((f) => (
            <div key={`f${f.id}`} className={chip}>
              <span>{f.uploading ? "⏳" : "📎"}</span>
              <span className="max-w-[12rem] truncate" title={f.name}>
                {f.name}
              </span>
              <span className="opacity-60">
                {f.uploading ? t("session.uploading") : fmtSize(f.size)}
              </span>
              <button
                onClick={() => removeFile(f.id)}
                aria-label={t("session.removeFile")}
                className="leading-none hover:text-danger"
              >
                ×
              </button>
            </div>
          ))}
        </div>
      )}
      <div className="flex flex-col gap-2 md:flex-row md:items-end">
      <input
        ref={fileInputRef}
        type="file"
        multiple
        className="hidden"
        onChange={(e) => {
          const picked = Array.from(e.target.files ?? []);
          if (picked.length) addFiles(picked);
          e.target.value = ""; // allow re-picking the same file
        }}
      />
      <textarea
        ref={textareaRef}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onPaste={(e) => {
          const pasted = Array.from(e.clipboardData.files);
          if (pasted.length > 0) {
            e.preventDefault();
            addFiles(pasted);
          }
        }}
        onDragOver={(e) => e.preventDefault()}
        onDrop={(e) => {
          const dropped = Array.from(e.dataTransfer.files);
          if (dropped.length > 0) {
            e.preventDefault();
            addFiles(dropped);
          }
        }}
        onKeyDown={(e) => {
          if (!isTouch && e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            submit();
          }
        }}
        rows={1}
        placeholder={disabled ? t("session.waitingForConnection") : t("session.messagePlaceholder")}
        className="w-full md:flex-1 resize-none bg-bg border border-border rounded-control px-3 py-2 text-sm focus:border-accent-dim outline-none min-h-[3.5rem] max-h-[40vh] overflow-y-auto"
      />
      {/* On mobile this row sits below the full-width textarea; `md:contents`
          dissolves it on desktop so the buttons re-join the textarea's flex row. */}
      <div className="flex gap-2 items-end justify-end md:contents">
      <button
        onClick={() => fileInputRef.current?.click()}
        disabled={disabled}
        aria-label={t("session.attachFiles")}
        title={t("session.attachFilesTitle")}
        className="rounded-control px-3 py-2 text-sm border border-border text-muted hover:text-fg transition-colors disabled:opacity-40"
      >
        📎
      </button>
      {voiceAvailable && (
        <button
          onClick={toggleMic}
          disabled={disabled}
          aria-label={listening ? t("session.stopDictation") : t("session.dictateMessage")}
          title={listening ? t("session.stopDictation") : t("session.dictateMessage")}
          className={`rounded-control px-3 py-2 text-sm border transition-colors disabled:opacity-40 ${
            listening
              ? "border-danger text-danger animate-pulse"
              : "border-border text-muted hover:text-fg"
          }`}
        >
          🎤
        </button>
      )}
      <Button onClick={submit} disabled={disabled || uploading || empty}>
        {t("session.send")}
      </Button>
      </div>
      </div>
    </div>
  );
}
