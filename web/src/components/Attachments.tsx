import { useEffect, useRef, useState, type ClipboardEvent, type DragEvent, type PointerEvent as ReactPointerEvent, type WheelEvent as ReactWheelEvent } from "react";
import { createPortal } from "react-dom";
import { Check, ChevronLeft, ChevronRight, Code2, Copy, Download, Eye, FileText, Loader2, Paperclip, RotateCcw, X, ZoomIn, ZoomOut } from "lucide-react";
import { api, type Attachment } from "../api";
import { CodeBlock, languageFromPath } from "./CodeHighlight";
import { MessageBody } from "./MessageBody";

const PREVIEWABLE_EXTS = new Set([
  "md", "markdown", "txt", "log", "csv", "tsv", "json", "yaml", "yml", "toml", "xml",
  "diff", "patch", "go", "ts", "tsx", "js", "jsx", "py", "rs", "sh", "bash", "zsh",
  "sql", "css", "html", "c", "cpp", "h", "java", "rb", "swift", "kt", "graphql",
]);
const MAX_INLINE_PREVIEW_BYTES = 1024 * 1024;

export function isImage(mediaType: string): boolean {
  return mediaType.startsWith("image/");
}

function fileExtension(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot >= 0 ? name.slice(dot + 1).toLowerCase() : "";
}

export function isMarkdownFile(name: string): boolean {
  const ext = fileExtension(name);
  return ext === "md" || ext === "markdown";
}

export function isPreviewableDocument(a: Attachment): boolean {
  if (isImage(a.media_type)) return false;
  if (a.media_type.startsWith("text/")) return true;
  if (a.media_type === "application/json" || a.media_type === "application/xml" || a.media_type === "application/javascript") return true;
  return PREVIEWABLE_EXTS.has(fileExtension(a.name));
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/** Picker + paste/drop + chip row shared by every composer that can attach files. */
export function useAttachmentDraft(spaceSlug: string) {
  const [pending, setPending] = useState<Attachment[]>([]);
  const [uploading, setUploading] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [error, setError] = useState("");
  const inputRef = useRef<HTMLInputElement | null>(null);

  async function addFiles(files: FileList | File[] | null) {
    const list = files ? Array.from(files) : [];
    if (list.length === 0) return;
    setError("");
    setUploading(true);
    try {
      for (const file of list) {
        const uploaded = await api.uploadAttachment(spaceSlug, file);
        setPending((prev) => [...prev, uploaded]);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Upload failed");
    } finally {
      setUploading(false);
      if (inputRef.current) inputRef.current.value = "";
    }
  }

  function handlePaste(e: ClipboardEvent) {
    const files = Array.from(e.clipboardData?.files ?? []);
    if (files.length === 0) return;
    e.preventDefault();
    void addFiles(files);
  }

  const dropProps = {
    onDragOver: (e: DragEvent) => {
      if (!Array.from(e.dataTransfer?.types ?? []).includes("Files")) return;
      e.preventDefault();
      setDragging(true);
    },
    onDragLeave: (e: DragEvent) => {
      if (e.currentTarget.contains(e.relatedTarget as Node | null)) return;
      setDragging(false);
    },
    onDrop: (e: DragEvent) => {
      setDragging(false);
      const files = Array.from(e.dataTransfer?.files ?? []);
      if (files.length === 0) return;
      e.preventDefault();
      void addFiles(files);
    },
  };

  function remove(id: string) {
    setPending((prev) => prev.filter((a) => a.id !== id));
  }

  function clear() {
    setPending([]);
    setError("");
  }

  return {
    spaceSlug,
    pending,
    uploading,
    dragging,
    error,
    inputRef,
    ids: pending.map((a) => a.id),
    addFiles,
    handlePaste,
    dropProps,
    remove,
    clear,
  };
}

export function AttachmentPicker({
  draft,
}: {
  draft: ReturnType<typeof useAttachmentDraft>;
}) {
  return (
    <>
      <input
        ref={draft.inputRef}
        type="file"
        multiple
        className="hidden"
        onChange={(e) => void draft.addFiles(e.target.files)}
      />
      <button
        type="button"
        onClick={() => draft.inputRef.current?.click()}
        disabled={draft.uploading}
        title="Attach files"
        aria-label="Attach files"
        className="size-7 rounded-[4px] hover:bg-hover text-fg-3 hover:text-fg flex items-center justify-center disabled:opacity-40 transition-colors"
      >
        {draft.uploading ? <Loader2 className="size-4 animate-spin" /> : <Paperclip className="size-4" />}
      </button>
    </>
  );
}

export function AttachmentDraftRow({
  draft,
}: {
  draft: ReturnType<typeof useAttachmentDraft>;
}) {
  if (draft.pending.length === 0 && !draft.error) return null;
  return (
    <div className="px-3 pb-1.5 flex flex-wrap items-center gap-1.5">
      {draft.pending.map((a) => (
        <span
          key={a.id}
          className="inline-flex items-center gap-1.5 h-8 pl-1.5 pr-1 rounded-[6px] border border-line bg-subtle text-[13px] text-fg-2 max-w-[260px] animate-pop-in"
        >
          {isImage(a.media_type) ? (
            <img
              src={api.attachmentURL(draft.spaceSlug, a.id)}
              alt=""
              className="size-6 rounded-[4px] object-cover border border-line bg-card shrink-0"
            />
          ) : (
            <FileText className="size-4 text-fg-3 shrink-0 ml-0.5" />
          )}
          <span className="truncate">{a.name}</span>
          <span className="tabular text-[12px] text-fg-4 shrink-0">{formatBytes(a.size_bytes)}</span>
          <button
            type="button"
            onClick={() => draft.remove(a.id)}
            aria-label={`Remove ${a.name}`}
            className="size-6 rounded-[4px] hover:bg-hover text-fg-3 flex items-center justify-center"
          >
            <X className="size-3.5" />
          </button>
        </span>
      ))}
      {draft.error && <span className="text-[13px] text-danger">{draft.error}</span>}
    </div>
  );
}

function ImageLightbox({
  spaceSlug,
  images,
  initialIndex,
  onClose,
}: {
  spaceSlug: string;
  images: Attachment[];
  initialIndex: number;
  onClose: () => void;
}) {
  const [index, setIndex] = useState(initialIndex);
  const [scale, setScale] = useState(1);
  const [pan, setPan] = useState({ x: 0, y: 0 });
  const dragRef = useRef<{ x: number; y: number; px: number; py: number } | null>(null);
  const attachment = images[index] ?? images[0];
  const hasMultiple = images.length > 1;

  const resetZoom = () => {
    setScale(1);
    setPan({ x: 0, y: 0 });
  };

  useEffect(() => {
    resetZoom();
  }, [index]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onClose();
      } else if (e.key === "ArrowLeft" && scale <= 1 && index > 0) {
        e.preventDefault();
        setIndex((i) => Math.max(0, i - 1));
      } else if (e.key === "ArrowRight" && scale <= 1 && index < images.length - 1) {
        e.preventDefault();
        setIndex((i) => Math.min(images.length - 1, i + 1));
      } else if (e.key === "0") {
        resetZoom();
      } else if (e.key === "+" || e.key === "=") {
        setScale((s) => Math.min(4, Number((s + 0.5).toFixed(2))));
      } else if (e.key === "-") {
        setScale((s) => {
          const next = Math.max(1, Number((s - 0.5).toFixed(2)));
          if (next <= 1) setPan({ x: 0, y: 0 });
          return next;
        });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, index, images.length, scale]);

  function onWheel(e: ReactWheelEvent) {
    e.preventDefault();
    const delta = e.deltaY < 0 ? 0.25 : -0.25;
    setScale((s) => {
      const next = Math.max(1, Math.min(4, Number((s + delta).toFixed(2))));
      if (next <= 1) setPan({ x: 0, y: 0 });
      return next;
    });
  }

  function onPointerDown(e: ReactPointerEvent<HTMLImageElement>) {
    if (scale <= 1) return;
    e.preventDefault();
    e.currentTarget.setPointerCapture(e.pointerId);
    dragRef.current = { x: e.clientX, y: e.clientY, px: pan.x, py: pan.y };
  }

  function onPointerMove(e: ReactPointerEvent<HTMLImageElement>) {
    if (!dragRef.current) return;
    setPan({
      x: dragRef.current.px + (e.clientX - dragRef.current.x),
      y: dragRef.current.py + (e.clientY - dragRef.current.y),
    });
  }

  function onPointerUp() {
    dragRef.current = null;
  }

  if (!attachment) return null;
  const url = api.attachmentURL(spaceSlug, attachment.id);

  return createPortal(
    <div
      role="dialog"
      aria-modal="true"
      aria-label={attachment.name}
      onClick={onClose}
      className="fixed inset-0 z-50 bg-black/80 backdrop-blur-xs flex flex-col items-center justify-center p-6 animate-fade-in"
    >
      {hasMultiple && (
        <>
          <button
            type="button"
            disabled={index === 0}
            aria-label="Previous image"
            onClick={(e) => {
              e.stopPropagation();
              setIndex((i) => Math.max(0, i - 1));
            }}
            className="fixed left-5 top-1/2 -translate-y-1/2 size-10 rounded-full bg-card/90 border border-line text-fg shadow-md flex items-center justify-center hover:bg-card disabled:opacity-30 transition-opacity"
          >
            <ChevronLeft className="size-5" />
          </button>
          <button
            type="button"
            disabled={index === images.length - 1}
            aria-label="Next image"
            onClick={(e) => {
              e.stopPropagation();
              setIndex((i) => Math.min(images.length - 1, i + 1));
            }}
            className="fixed right-5 top-1/2 -translate-y-1/2 size-10 rounded-full bg-card/90 border border-line text-fg shadow-md flex items-center justify-center hover:bg-card disabled:opacity-30 transition-opacity"
          >
            <ChevronRight className="size-5" />
          </button>
        </>
      )}
      <div
        onClick={(e) => e.stopPropagation()}
        className="max-w-[90vw] max-h-[88vh] flex flex-col items-center gap-3"
      >
        <div
          onWheel={onWheel}
          className="relative max-w-[86vw] max-h-[72vh] overflow-hidden rounded-[8px] shadow-lg checkerboard-bg flex items-center justify-center"
        >
          <img
            src={url}
            alt={attachment.name}
            draggable={false}
            onDoubleClick={() => {
              if (scale > 1) resetZoom();
              else setScale(2);
            }}
            onPointerDown={onPointerDown}
            onPointerMove={onPointerMove}
            onPointerUp={onPointerUp}
            onPointerCancel={onPointerUp}
            style={{
              transform: `translate(${pan.x}px, ${pan.y}px) scale(${scale})`,
              cursor: scale > 1 ? "grab" : "zoom-in",
            }}
            className="max-w-[86vw] max-h-[72vh] object-contain select-none transition-transform duration-100"
          />
        </div>
        <div className="px-3.5 py-2 rounded-[8px] bg-card border border-line shadow-md flex items-center gap-2.5 text-[13px] flex-wrap justify-center">
          <span className="font-medium text-fg truncate max-w-[260px]">{attachment.name}</span>
          {hasMultiple && (
            <span className="font-mono tabular text-[12px] text-fg-3 shrink-0">
              {index + 1} / {images.length}
            </span>
          )}
          <span className="tabular text-fg-3 shrink-0">{formatBytes(attachment.size_bytes)}</span>
          <span className="w-px h-4 bg-line shrink-0" />
          <div className="flex items-center gap-0.5">
            <button
              type="button"
              aria-label="Zoom out"
              disabled={scale <= 1}
              onClick={() => {
                setScale((s) => {
                  const next = Math.max(1, Number((s - 0.5).toFixed(2)));
                  if (next <= 1) setPan({ x: 0, y: 0 });
                  return next;
                });
              }}
              className="size-7 rounded-[5px] hover:bg-hover text-fg-2 disabled:opacity-35 inline-flex items-center justify-center"
            >
              <ZoomOut className="size-3.5" />
            </button>
            <button
              type="button"
              aria-label="Reset zoom"
              onClick={resetZoom}
              className="h-7 px-1.5 rounded-[5px] hover:bg-hover font-mono tabular text-[12px] text-fg-2 inline-flex items-center gap-1"
            >
              {Math.round(scale * 100)}%
              {scale > 1 && <RotateCcw className="size-3 text-fg-4" />}
            </button>
            <button
              type="button"
              aria-label="Zoom in"
              disabled={scale >= 4}
              onClick={() => setScale((s) => Math.min(4, Number((s + 0.5).toFixed(2))))}
              className="size-7 rounded-[5px] hover:bg-hover text-fg-2 disabled:opacity-35 inline-flex items-center justify-center"
            >
              <ZoomIn className="size-3.5" />
            </button>
          </div>
          <a
            href={url}
            download={attachment.name}
            className="h-7 px-2.5 rounded-[5px] bg-subtle hover:bg-hover text-fg inline-flex items-center gap-1.5 font-medium transition-colors"
          >
            <Download className="size-3.5" />
            Download
          </a>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close preview"
            className="size-7 rounded-[5px] hover:bg-hover text-fg-3 hover:text-fg inline-flex items-center justify-center"
          >
            <X className="size-4" />
          </button>
        </div>
        {hasMultiple && (
          <div className="flex items-center gap-1.5 px-2 py-1.5 rounded-[8px] bg-card/90 border border-line max-w-[80vw] overflow-x-auto">
            {images.map((img, i) => (
              <button
                key={img.id}
                type="button"
                aria-label={`View image ${i + 1}: ${img.name}`}
                onClick={() => setIndex(i)}
                className={`size-10 rounded-[5px] overflow-hidden border transition-all shrink-0 checkerboard-bg ${
                  i === index ? "border-accent ring-2 ring-accent/40" : "border-line opacity-65 hover:opacity-100"
                }`}
              >
                <img src={api.attachmentURL(spaceSlug, img.id)} alt="" className="size-full object-cover" />
              </button>
            ))}
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}

/** In-app preview modal for text, Markdown, JSON, and code attachments reusing the shared renderer. */
function DocumentPreviewModal({
  spaceSlug,
  attachment,
  onClose,
}: {
  spaceSlug: string;
  attachment: Attachment;
  onClose: () => void;
}) {
  const [content, setContent] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [mdMode, setMdMode] = useState<"preview" | "raw">("preview");
  const [copied, setCopied] = useState(false);
  const url = api.attachmentURL(spaceSlug, attachment.id);
  const md = isMarkdownFile(attachment.name);
  const lang = languageFromPath(attachment.name);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  useEffect(() => {
    if (attachment.size_bytes > MAX_INLINE_PREVIEW_BYTES) {
      setLoading(false);
      setError("File is larger than 1 MB. Download it to inspect locally.");
      return;
    }
    let cancelled = false;
    setLoading(true);
    setError(null);
    void fetch(url, { credentials: "include" })
      .then(async (resp) => {
        if (!resp.ok) throw new Error(`Failed to load file (HTTP ${resp.status})`);
        return resp.text();
      })
      .then((text) => {
        if (!cancelled) setContent(text);
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Could not load file");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [url, attachment.size_bytes]);

  return createPortal(
    <div
      role="dialog"
      aria-modal="true"
      aria-label={attachment.name}
      onClick={onClose}
      className="fixed inset-0 z-50 bg-black/55 flex items-center justify-center p-6 animate-fade-in"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-[840px] max-h-[82vh] rounded-[8px] bg-panel border border-line-strong shadow-lg flex flex-col overflow-hidden animate-pop-in"
      >
        <div className="h-12 px-4 border-b border-line flex items-center justify-between gap-3 shrink-0">
          <div className="flex items-center gap-2 min-w-0">
            <FileText className="size-4 text-fg-3 shrink-0" />
            <span className="text-[14px] font-semibold text-fg truncate">{attachment.name}</span>
            <span className="tabular text-[12px] text-fg-4 shrink-0">{formatBytes(attachment.size_bytes)}</span>
          </div>
          <div className="flex items-center gap-1.5 shrink-0">
            {md && content !== null && (
              <div className="p-0.5 rounded-[6px] bg-subtle border border-line flex items-center">
                <button
                  type="button"
                  onClick={() => setMdMode("preview")}
                  className={`h-6 px-2 rounded-[4px] text-[11.5px] font-medium transition-colors ${
                    mdMode === "preview" ? "bg-card text-fg shadow-xs" : "text-fg-3 hover:text-fg"
                  }`}
                >
                  Preview
                </button>
                <button
                  type="button"
                  onClick={() => setMdMode("raw")}
                  className={`h-6 px-2 rounded-[4px] text-[11.5px] font-medium inline-flex items-center gap-1 transition-colors ${
                    mdMode === "raw" ? "bg-card text-fg shadow-xs" : "text-fg-3 hover:text-fg"
                  }`}
                >
                  <Code2 className="size-3" />
                  Raw
                </button>
              </div>
            )}
            {content !== null && (
              <button
                type="button"
                onClick={() => {
                  void navigator.clipboard?.writeText(content).then(() => {
                    setCopied(true);
                    window.setTimeout(() => setCopied(false), 1500);
                  });
                }}
                aria-label="Copy file content"
                className="h-7 px-2.5 rounded-[5px] bg-subtle hover:bg-hover text-fg-2 hover:text-fg inline-flex items-center gap-1.5 text-[12.5px] font-medium transition-colors"
              >
                {copied ? <Check className="size-3.5 text-work" /> : <Copy className="size-3.5" />}
                {copied ? "Copied" : "Copy"}
              </button>
            )}
            <a
              href={url}
              download={attachment.name}
              className="h-7 px-2.5 rounded-[5px] bg-subtle hover:bg-hover text-fg inline-flex items-center gap-1.5 text-[12.5px] font-medium transition-colors"
            >
              <Download className="size-3.5" />
              Download
            </a>
            <button
              type="button"
              onClick={onClose}
              aria-label="Close document preview"
              className="size-7 rounded-[5px] hover:bg-hover text-fg-3 hover:text-fg inline-flex items-center justify-center"
            >
              <X className="size-4" />
            </button>
          </div>
        </div>
        <div className="flex-1 min-h-0 overflow-auto scroll-thin bg-bg/40">
          {loading ? (
            <div className="h-44 flex items-center justify-center gap-2 text-[13.5px] text-fg-3">
              <Loader2 className="size-4 animate-spin" />
              Loading preview…
            </div>
          ) : error ? (
            <div className="p-6 text-center text-[13.5px] text-fg-3">{error}</div>
          ) : content !== null && md && mdMode === "preview" ? (
            <div className="p-5 text-[14.5px] leading-relaxed text-fg md">
              <MessageBody body={content} members={[]} />
            </div>
          ) : content !== null ? (
            <CodeBlock code={content} language={lang} showLineNumbers bare />
          ) : null}
        </div>
      </div>
    </div>,
    document.body,
  );
}

function InlineImage({
  spaceSlug,
  attachment,
  onPreview,
}: {
  spaceSlug: string;
  attachment: Attachment;
  onPreview: () => void;
}) {
  const [loaded, setLoaded] = useState(false);
  return (
    <button
      type="button"
      onClick={onPreview}
      aria-label={`Preview ${attachment.name}`}
      className={`block border border-line rounded-[7px] overflow-hidden checkerboard-bg hover:shadow-md transition-shadow ${
        loaded ? "" : "min-w-[180px] min-h-[120px]"
      }`}
    >
      <img
        src={api.attachmentURL(spaceSlug, attachment.id)}
        alt={attachment.name}
        loading="lazy"
        onLoad={() => setLoaded(true)}
        onError={() => setLoaded(true)}
        className={`max-h-[280px] max-w-[360px] object-contain block transition-opacity duration-150 ${
          loaded ? "opacity-100" : "opacity-0"
        }`}
      />
    </button>
  );
}

/** Renders already-sent attachments: inline images with zoomable gallery lightbox, previewable docs, and downloads. */
export function MessageAttachments({
  spaceSlug,
  attachments,
}: {
  spaceSlug: string;
  attachments?: Attachment[];
}) {
  const [imagePreviewIdx, setImagePreviewIdx] = useState<number | null>(null);
  const [docPreview, setDocPreview] = useState<Attachment | null>(null);
  if (!attachments || attachments.length === 0) return null;
  const images = attachments.filter((a) => isImage(a.media_type));
  const files = attachments.filter((a) => !isImage(a.media_type));

  return (
    <div className="mt-2 space-y-2">
      {images.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {images.map((a, idx) => (
            <InlineImage key={a.id} spaceSlug={spaceSlug} attachment={a} onPreview={() => setImagePreviewIdx(idx)} />
          ))}
        </div>
      )}
      {files.length > 0 && (
        <div className="border border-line rounded-[7px] divide-y divide-line overflow-hidden max-w-[460px] bg-card">
          {files.map((a) => {
            const previewable = isPreviewableDocument(a);
            const url = api.attachmentURL(spaceSlug, a.id);
            return (
              <div key={a.id} className="h-11 px-3 flex items-center justify-between gap-2 hover:bg-subtle text-[14px] transition-colors">
                {previewable ? (
                  <button
                    type="button"
                    onClick={() => setDocPreview(a)}
                    aria-label={`Preview ${a.name}`}
                    className="flex items-center gap-2 min-w-0 flex-1 text-left"
                  >
                    <FileText className="size-4 text-fg-3 shrink-0" />
                    <span className="truncate text-fg">{a.name}</span>
                  </button>
                ) : (
                  <a href={url} download={a.name} className="flex items-center gap-2 min-w-0 flex-1">
                    <FileText className="size-4 text-fg-3 shrink-0" />
                    <span className="truncate text-fg">{a.name}</span>
                  </a>
                )}
                <span className="tabular text-[12.5px] text-fg-3 shrink-0">{formatBytes(a.size_bytes)}</span>
                {previewable && (
                  <button
                    type="button"
                    onClick={() => setDocPreview(a)}
                    title={`Preview ${a.name}`}
                    aria-label={`Open ${a.name}`}
                    className="size-7 rounded-[5px] text-fg-3 hover:text-fg hover:bg-hover inline-flex items-center justify-center shrink-0"
                  >
                    <Eye className="size-3.5" />
                  </button>
                )}
                <a
                  href={url}
                  download={a.name}
                  title={`Download ${a.name}`}
                  aria-label={`Download ${a.name}`}
                  className="size-7 rounded-[5px] text-fg-3 hover:text-fg hover:bg-hover inline-flex items-center justify-center shrink-0"
                >
                  <Download className="size-3.5" />
                </a>
              </div>
            );
          })}
        </div>
      )}
      {imagePreviewIdx !== null && (
        <ImageLightbox
          spaceSlug={spaceSlug}
          images={images}
          initialIndex={imagePreviewIdx}
          onClose={() => setImagePreviewIdx(null)}
        />
      )}
      {docPreview && (
        <DocumentPreviewModal
          spaceSlug={spaceSlug}
          attachment={docPreview}
          onClose={() => setDocPreview(null)}
        />
      )}
    </div>
  );
}
