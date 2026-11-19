import { useRef, useState } from "react";
import { FileText, Loader2, Paperclip, X } from "lucide-react";
import { api, type Attachment } from "../api";

export function isImage(mediaType: string): boolean {
  return mediaType.startsWith("image/");
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/** Picker + chip row shared by every composer that can attach files. */
export function useAttachmentDraft(spaceSlug: string) {
  const [pending, setPending] = useState<Attachment[]>([]);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState("");
  const inputRef = useRef<HTMLInputElement | null>(null);

  async function addFiles(files: FileList | null) {
    if (!files || files.length === 0) return;
    setError("");
    setUploading(true);
    try {
      for (const file of Array.from(files)) {
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
    error,
    inputRef,
    ids: pending.map((a) => a.id),
    addFiles,
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
        className="h-6 w-6 rounded-[5px] hover:bg-[#E9E6DE] text-[#6E6B64] hover:text-[#161614] flex items-center justify-center disabled:opacity-40"
      >
        {draft.uploading ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Paperclip className="w-3.5 h-3.5" />}
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
    <div className="px-3 pb-1 flex flex-wrap items-center gap-1.5">
      {draft.pending.map((a) => (
        <span
          key={a.id}
          className="inline-flex items-center gap-1.5 h-6 pl-1.5 pr-1 rounded border border-[#E4E1D9] bg-[#FAF9F6] text-[11.5px] text-[#45433E] max-w-[240px]"
        >
          <FileText className="w-3 h-3 text-[#6E6B64] shrink-0" />
          <span className="truncate">{a.name}</span>
          <span className="font-mono-num text-[10.5px] text-[#8A867D] shrink-0">{formatBytes(a.size_bytes)}</span>
          <button
            type="button"
            onClick={() => draft.remove(a.id)}
            aria-label={`Remove ${a.name}`}
            className="w-4 h-4 rounded hover:bg-[#E4E1D9] text-[#6E6B64] flex items-center justify-center"
          >
            <X className="w-2.5 h-2.5" />
          </button>
        </span>
      ))}
      {draft.error && <span className="text-[11.5px] text-[#A83224]">{draft.error}</span>}
    </div>
  );
}

/** Renders already-sent attachments: inline images, everything else a download row. */
export function MessageAttachments({
  spaceSlug,
  attachments,
}: {
  spaceSlug: string;
  attachments?: Attachment[];
}) {
  if (!attachments || attachments.length === 0) return null;
  const images = attachments.filter((a) => isImage(a.media_type));
  const files = attachments.filter((a) => !isImage(a.media_type));

  return (
    <div className="mt-1.5 space-y-1.5">
      {images.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {images.map((a) => (
            <a
              key={a.id}
              href={api.attachmentURL(spaceSlug, a.id)}
              download={a.name}
              className="block border border-[#E4E1D9] rounded-[6px] overflow-hidden bg-[#FAF9F6]"
            >
              <img
                src={api.attachmentURL(spaceSlug, a.id)}
                alt={a.name}
                loading="lazy"
                className="max-h-[240px] max-w-[320px] object-contain block"
              />
            </a>
          ))}
        </div>
      )}
      {files.length > 0 && (
        <div className="border border-[#E4E1D9] rounded-[6px] divide-y divide-[#E4E1D9] overflow-hidden max-w-[420px]">
          {files.map((a) => (
            <a
              key={a.id}
              href={api.attachmentURL(spaceSlug, a.id)}
              download={a.name}
              className="h-8 px-2.5 flex items-center justify-between gap-3 bg-[#FAF9F6] hover:bg-[#F2F0EA] text-[12.5px]"
            >
              <span className="flex items-center gap-2 min-w-0">
                <FileText className="w-3.5 h-3.5 text-[#6E6B64] shrink-0" />
                <span className="truncate text-[#161614]">{a.name}</span>
              </span>
              <span className="font-mono-num text-[11px] text-[#8A867D] shrink-0">{formatBytes(a.size_bytes)}</span>
            </a>
          ))}
        </div>
      )}
    </div>
  );
}
