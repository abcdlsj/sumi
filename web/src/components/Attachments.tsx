import { useEffect, useRef, useState, type ClipboardEvent, type DragEvent } from "react";
import { createPortal } from "react-dom";
import { Download, FileText, Loader2, Paperclip, X } from "lucide-react";
import { api, type Attachment } from "../api";

export function isImage(mediaType: string): boolean {
  return mediaType.startsWith("image/");
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
        className="size-8 rounded-[8px] hover:bg-hover text-fg-3 hover:text-fg flex items-center justify-center disabled:opacity-40 transition-colors"
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
          className="inline-flex items-center gap-1.5 h-8 pl-2 pr-1 rounded-[8px] border border-line bg-subtle text-[13px] text-fg-2 max-w-[260px] animate-pop-in"
        >
          <FileText className="size-4 text-fg-3 shrink-0" />
          <span className="truncate">{a.name}</span>
          <span className="tabular text-[12px] text-fg-4 shrink-0">{formatBytes(a.size_bytes)}</span>
          <button
            type="button"
            onClick={() => draft.remove(a.id)}
            aria-label={`Remove ${a.name}`}
            className="size-6 rounded-[6px] hover:bg-hover text-fg-3 flex items-center justify-center"
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
  attachment,
  onClose,
}: {
  spaceSlug: string;
  attachment: Attachment;
  onClose: () => void;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const url = api.attachmentURL(spaceSlug, attachment.id);
  return createPortal(
    <div
      role="dialog"
      aria-modal="true"
      aria-label={attachment.name}
      onClick={onClose}
      className="fixed inset-0 z-50 bg-black/75 backdrop-blur-xs flex flex-col items-center justify-center p-6 animate-fade-in"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="max-w-[92vw] max-h-[88vh] flex flex-col items-center gap-3"
      >
        <img
          src={url}
          alt={attachment.name}
          className="max-w-full max-h-[78vh] object-contain rounded-[10px] shadow-lg bg-card"
        />
        <div className="px-3.5 py-2 rounded-[10px] bg-card border border-line shadow-md flex items-center gap-3 text-[13px]">
          <span className="font-medium text-fg truncate max-w-[320px]">{attachment.name}</span>
          <span className="tabular text-fg-3 shrink-0">{formatBytes(attachment.size_bytes)}</span>
          <a
            href={url}
            download={attachment.name}
            className="h-7 px-2.5 rounded-[7px] bg-subtle hover:bg-hover text-fg inline-flex items-center gap-1.5 font-medium transition-colors"
          >
            <Download className="size-3.5" />
            Download
          </a>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close preview"
            className="size-7 rounded-[7px] hover:bg-hover text-fg-3 hover:text-fg inline-flex items-center justify-center"
          >
            <X className="size-4" />
          </button>
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
  onPreview: (a: Attachment) => void;
}) {
  const [loaded, setLoaded] = useState(false);
  return (
    <button
      type="button"
      onClick={() => onPreview(attachment)}
      aria-label={`Preview ${attachment.name}`}
      className={`block border border-line rounded-[10px] overflow-hidden bg-subtle hover:shadow-md transition-shadow ${
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

/** Renders already-sent attachments: inline images with lightbox preview, other files as download rows. */
export function MessageAttachments({
  spaceSlug,
  attachments,
}: {
  spaceSlug: string;
  attachments?: Attachment[];
}) {
  const [preview, setPreview] = useState<Attachment | null>(null);
  if (!attachments || attachments.length === 0) return null;
  const images = attachments.filter((a) => isImage(a.media_type));
  const files = attachments.filter((a) => !isImage(a.media_type));

  return (
    <div className="mt-2 space-y-2">
      {images.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {images.map((a) => (
            <InlineImage key={a.id} spaceSlug={spaceSlug} attachment={a} onPreview={setPreview} />
          ))}
        </div>
      )}
      {files.length > 0 && (
        <div className="border border-line rounded-[10px] divide-y divide-line overflow-hidden max-w-[440px] bg-card">
          {files.map((a) => (
            <a
              key={a.id}
              href={api.attachmentURL(spaceSlug, a.id)}
              download={a.name}
              className="h-11 px-3 flex items-center justify-between gap-3 hover:bg-subtle text-[14px] transition-colors"
            >
              <span className="flex items-center gap-2 min-w-0">
                <FileText className="size-4 text-fg-3 shrink-0" />
                <span className="truncate text-fg">{a.name}</span>
              </span>
              <span className="tabular text-[12.5px] text-fg-3 shrink-0">{formatBytes(a.size_bytes)}</span>
            </a>
          ))}
        </div>
      )}
      {preview && <ImageLightbox spaceSlug={spaceSlug} attachment={preview} onClose={() => setPreview(null)} />}
    </div>
  );
}
