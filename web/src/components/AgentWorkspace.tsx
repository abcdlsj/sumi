import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  AlertTriangle,
  ArrowLeft,
  Check,
  ChevronDown,
  ChevronRight,
  Code2,
  Copy,
  Eye,
  EyeOff,
  File,
  FileCode2,
  FileText,
  Folder,
  FolderOpen,
  Image as ImageIcon,
  Link2,
  Loader2,
  Maximize2,
  RefreshCw,
  WifiOff,
  X,
} from "lucide-react";
import {
  api,
  type BootstrapData,
  type Computer,
  type Member,
  type WorkspaceFileContent,
  type WorkspaceFileNode,
} from "../api";
import { MessageBody } from "./MessageBody";
import { IconButton } from "./ui";

const IMAGE_EXTS = new Set(["png", "jpg", "jpeg", "gif", "webp", "avif"]);
const CODE_EXTS = new Set(["ts", "tsx", "js", "jsx", "go", "py", "rs", "sh", "bash", "zsh", "json", "yaml", "yml", "toml", "sql", "css", "html", "xml", "c", "cpp", "h"]);

function fileExt(name: string): string {
  const idx = name.lastIndexOf(".");
  return idx >= 0 ? name.slice(idx + 1).toLowerCase() : "";
}

function isMarkdown(path: string): boolean {
  const ext = fileExt(path);
  return ext === "md" || ext === "markdown" || ext === "mdx";
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatMtime(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  const diff = Date.now() - ms;
  if (diff < 60_000) return "just now";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function EntryIcon({ entry, open }: { entry: WorkspaceFileNode; open: boolean }) {
  if (entry.is_symlink) return <Link2 className="size-3.5 text-fg-4 shrink-0" />;
  if (entry.is_dir) {
    return open ? <FolderOpen className="size-3.5 text-accent-text shrink-0" /> : <Folder className="size-3.5 text-fg-3 shrink-0" />;
  }
  const ext = fileExt(entry.name);
  if (IMAGE_EXTS.has(ext)) return <ImageIcon className="size-3.5 text-fg-3 shrink-0" />;
  if (ext === "md" || ext === "markdown" || ext === "txt") return <FileText className="size-3.5 text-fg-3 shrink-0" />;
  if (CODE_EXTS.has(ext)) return <FileCode2 className="size-3.5 text-fg-3 shrink-0" />;
  return <File className="size-3.5 text-fg-3 shrink-0" />;
}

export function AgentWorkspace({
  spaceSlug,
  member,
  computer,
  boot,
}: {
  spaceSlug: string;
  member: Member;
  computer?: Computer;
  boot: BootstrapData;
}) {
  const [includeHidden, setIncludeHidden] = useState(false);
  const [rootPath, setRootPath] = useState("");
  const [dirEntries, setDirEntries] = useState<Record<string, WorkspaceFileNode[]>>({});
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [loadingDirs, setLoadingDirs] = useState<Record<string, boolean>>({});
  const [rootLoading, setRootLoading] = useState(true);
  const [rootError, setRootError] = useState<string | null>(null);

  const [selectedFile, setSelectedFile] = useState<string | null>(null);
  const [fileContent, setFileContent] = useState<WorkspaceFileContent | null>(null);
  const [fileLoading, setFileLoading] = useState(false);
  const [fileError, setFileError] = useState<string | null>(null);
  const [mdMode, setMdMode] = useState<"preview" | "raw">("preview");
  const [copiedPath, setCopiedPath] = useState(false);
  const [copiedFile, setCopiedFile] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);

  const listSeqRef = useRef(0);
  const fileSeqRef = useRef(0);

  const loadDir = useCallback(
    async (dirPath: string, hidden: boolean, isRoot = false) => {
      const seq = ++listSeqRef.current;
      if (isRoot) {
        setRootLoading(true);
        setRootError(null);
      } else {
        setLoadingDirs((prev) => ({ ...prev, [dirPath]: true }));
      }
      try {
        const res = await api.listAgentWorkspace(spaceSlug, member.id, dirPath, hidden);
        if (isRoot && seq !== listSeqRef.current) return;
        if (isRoot) setRootPath(res.root_path);
        setDirEntries((prev) => ({ ...prev, [dirPath]: res.entries }));
      } catch (err) {
        const msg = err instanceof Error ? err.message : "Failed to load workspace";
        if (isRoot) {
          setRootError(msg);
          setDirEntries({});
        }
      } finally {
        if (isRoot) {
          setRootLoading(false);
        } else {
          setLoadingDirs((prev) => ({ ...prev, [dirPath]: false }));
        }
      }
    },
    [spaceSlug, member.id],
  );

  const openFile = useCallback(
    async (relPath: string) => {
      const seq = ++fileSeqRef.current;
      setSelectedFile(relPath);
      setFileLoading(true);
      setFileError(null);
      setFileContent(null);
      setMdMode("preview");
      try {
        const res = await api.readAgentWorkspaceFile(spaceSlug, member.id, relPath);
        if (seq !== fileSeqRef.current) return;
        setFileContent(res);
      } catch (err) {
        if (seq !== fileSeqRef.current) return;
        setFileError(err instanceof Error ? err.message : "Failed to read file");
      } finally {
        if (seq === fileSeqRef.current) setFileLoading(false);
      }
    },
    [spaceSlug, member.id],
  );

  useEffect(() => {
    setExpanded({});
    setDirEntries({});
    setSelectedFile(null);
    setFileContent(null);
    setFileError(null);
    void loadDir("", includeHidden, true);
  }, [member.id, includeHidden, loadDir]);

  useEffect(() => {
    if (!fullscreen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        setFullscreen(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [fullscreen]);

  function refresh() {
    const openDirs = Object.entries(expanded)
      .filter(([, isOpen]) => isOpen)
      .map(([dirPath]) => dirPath);
    void loadDir("", includeHidden, true);
    for (const dirPath of openDirs) {
      void loadDir(dirPath, includeHidden, false);
    }
    if (selectedFile) void openFile(selectedFile);
  }

  function toggleDir(dirPath: string) {
    const nextOpen = !expanded[dirPath];
    setExpanded((prev) => ({ ...prev, [dirPath]: nextOpen }));
    if (nextOpen && !dirEntries[dirPath] && !loadingDirs[dirPath]) {
      void loadDir(dirPath, includeHidden, false);
    }
  }

  function copyRootPath() {
    if (!rootPath) return;
    void navigator.clipboard?.writeText(rootPath).then(() => {
      setCopiedPath(true);
      window.setTimeout(() => setCopiedPath(false), 1500);
    });
  }

  function copyFileContent() {
    if (!fileContent || fileContent.binary || fileContent.encoding === "base64") return;
    void navigator.clipboard?.writeText(fileContent.content).then(() => {
      setCopiedFile(true);
      window.setTimeout(() => setCopiedFile(false), 1500);
    });
  }

  const rootItems = dirEntries[""] ?? [];
  const offline = computer && computer.connection_status !== "online";

  function renderTree(dirPath: string, depth: number) {
    const entries = dirEntries[dirPath] ?? [];
    if (entries.length === 0 && depth > 0) {
      return (
        <div style={{ paddingLeft: `${depth * 14 + 26}px` }} className="py-1 text-[12px] text-fg-4 italic">
          Empty folder
        </div>
      );
    }
    return entries.map((entry) => {
      const isOpen = Boolean(expanded[entry.path]);
      const isSelected = selectedFile === entry.path;
      const isLoading = Boolean(loadingDirs[entry.path]);
      return (
        <div key={entry.path}>
          <button
            type="button"
            disabled={entry.is_symlink}
            title={entry.is_symlink ? "Symlinks cannot be opened" : entry.path}
            onClick={() => (entry.is_dir ? toggleDir(entry.path) : void openFile(entry.path))}
            style={{ paddingLeft: `${depth * 14 + 8}px` }}
            className={`w-full pr-2.5 h-8 flex items-center gap-1.5 text-left rounded-[7px] text-[13px] transition-colors ${
              entry.is_symlink
                ? "text-fg-4 cursor-not-allowed opacity-70"
                : isSelected
                  ? "bg-accent-soft text-accent-text font-medium"
                  : "text-fg-2 hover:bg-hover hover:text-fg"
            }`}
          >
            <span className="size-4 flex items-center justify-center shrink-0 text-fg-4">
              {entry.is_dir ? (
                isLoading ? (
                  <Loader2 className="size-3 animate-spin" />
                ) : isOpen ? (
                  <ChevronDown className="size-3.5" />
                ) : (
                  <ChevronRight className="size-3.5" />
                )
              ) : null}
            </span>
            <EntryIcon entry={entry} open={isOpen} />
            <span className="truncate flex-1">{entry.name}</span>
            {!entry.is_dir && !entry.is_symlink && (
              <span className="tabular text-[11px] text-fg-4 shrink-0">{formatBytes(entry.size)}</span>
            )}
          </button>
          {entry.is_dir && isOpen && !isLoading && renderTree(entry.path, depth + 1)}
        </div>
      );
    });
  }

  function renderFileViewer(showBack: boolean) {
    if (!selectedFile) {
      return (
        <div className="flex-1 flex flex-col items-center justify-center px-6 text-center text-fg-3">
          <FolderOpen className="size-7 text-fg-4 mb-2" />
          <div className="text-[13.5px] font-medium text-fg-2">Select a file to inspect</div>
          <div className="mt-1 text-[12.5px] text-fg-4">Read-only view of {member.display_name}&apos;s workspace.</div>
        </div>
      );
    }
    const md = isMarkdown(selectedFile);
    const fileName = selectedFile.split("/").pop() ?? selectedFile;
    const isText = Boolean(fileContent && !fileContent.binary && fileContent.encoding !== "base64");

    return (
      <div className="flex-1 min-w-0 min-h-0 flex flex-col">
        <div className="px-3 py-2 border-b border-line flex items-center gap-1.5 bg-subtle/40 shrink-0">
          {showBack && (
            <button
              type="button"
              onClick={() => setSelectedFile(null)}
              className="h-7 px-2 rounded-[7px] text-[12.5px] text-fg-2 hover:text-fg hover:bg-hover inline-flex items-center gap-1 shrink-0 transition-colors"
            >
              <ArrowLeft className="size-3.5" />
              Files
            </button>
          )}
          <div className="min-w-0 flex-1">
            <div className="text-[13px] font-medium text-fg truncate" title={selectedFile}>
              {fileName}
            </div>
            {selectedFile.includes("/") && (
              <div className="text-[11px] font-mono text-fg-4 truncate">{selectedFile}</div>
            )}
          </div>
          {md && isText && (
            <div className="p-0.5 rounded-[7px] bg-subtle border border-line flex items-center shrink-0">
              <button
                type="button"
                onClick={() => setMdMode("preview")}
                className={`h-6 px-2 rounded-[5px] text-[11.5px] font-medium transition-colors ${
                  mdMode === "preview" ? "bg-card text-fg shadow-xs" : "text-fg-3 hover:text-fg"
                }`}
              >
                Preview
              </button>
              <button
                type="button"
                onClick={() => setMdMode("raw")}
                className={`h-6 px-2 rounded-[5px] text-[11.5px] font-medium inline-flex items-center gap-1 transition-colors ${
                  mdMode === "raw" ? "bg-card text-fg shadow-xs" : "text-fg-3 hover:text-fg"
                }`}
              >
                <Code2 className="size-3" />
                Raw
              </button>
            </div>
          )}
          {isText && (
            <IconButton label={copiedFile ? "Copied" : "Copy file"} onClick={copyFileContent}>
              {copiedFile ? <Check className="text-work" /> : <Copy />}
            </IconButton>
          )}
          {!fullscreen && (
            <IconButton label="Expand workspace viewer" onClick={() => setFullscreen(true)}>
              <Maximize2 />
            </IconButton>
          )}
        </div>

        <div className="flex-1 min-h-0 overflow-auto scroll-thin bg-canvas/40">
          {fileLoading ? (
            <div className="h-40 flex items-center justify-center gap-2 text-[13px] text-fg-3">
              <Loader2 className="size-4 animate-spin" />
              Loading file…
            </div>
          ) : fileError ? (
            <div className="p-5 flex items-start gap-2.5 text-[13px] text-danger">
              <AlertTriangle className="size-4 shrink-0 mt-0.5" />
              <div className="min-w-0 break-words">{fileError}</div>
            </div>
          ) : fileContent?.binary ? (
            <div className="h-48 px-6 flex flex-col items-center justify-center text-center text-fg-3">
              <File className="size-7 text-fg-4 mb-2" />
              <div className="text-[13.5px] font-medium text-fg-2">Binary file</div>
              <div className="mt-1 text-[12.5px] text-fg-4">Preview is not available for binary files.</div>
            </div>
          ) : fileContent?.encoding === "base64" && fileContent.mime_type ? (
            <div className="p-4 flex items-center justify-center min-h-[200px]">
              <img
                src={`data:${fileContent.mime_type};base64,${fileContent.content}`}
                alt={fileName}
                className="max-w-full max-h-[65vh] rounded-[8px] border border-line object-contain bg-card"
              />
            </div>
          ) : fileContent && md && mdMode === "preview" ? (
            <div className="p-4 text-[14px] leading-relaxed text-fg md">
              <MessageBody body={fileContent.content} members={boot.members} tasks={boot.tasks} />
            </div>
          ) : fileContent ? (
            <pre className="p-3.5 text-[12.5px] leading-[1.6] font-mono text-fg whitespace-pre overflow-x-auto">
              {fileContent.content || <span className="text-fg-4 italic">(empty file)</span>}
            </pre>
          ) : null}
        </div>

        {fileContent && (
          <div className="px-3 py-1.5 border-t border-line bg-panel flex items-center justify-between text-[11.5px] text-fg-4 shrink-0">
            <span className="tabular">{formatBytes(fileContent.size)}</span>
            {fileContent.mtime_ms > 0 && <span>Modified {formatMtime(fileContent.mtime_ms)}</span>}
          </div>
        )}
      </div>
    );
  }

  function renderTreePane() {
    if (rootLoading) {
      return (
        <div className="flex-1 flex items-center justify-center gap-2 text-[13px] text-fg-3">
          <Loader2 className="size-4 animate-spin" />
          Loading workspace…
        </div>
      );
    }
    if (rootError) {
      return (
        <div className="flex-1 p-5 flex flex-col items-center justify-center text-center">
          {offline ? <WifiOff className="size-6 text-fg-4 mb-2" /> : <AlertTriangle className="size-6 text-fg-4 mb-2" />}
          <div className="text-[13.5px] font-medium text-fg-2">
            {offline ? "Computer is offline" : "Workspace unavailable"}
          </div>
          <div className="mt-1 text-[12.5px] text-fg-3 max-w-[260px] break-words">{rootError}</div>
          <button
            type="button"
            onClick={refresh}
            className="mt-3 h-7 px-3 rounded-[7px] border border-line bg-card text-[12.5px] text-fg-2 hover:text-fg hover:bg-hover inline-flex items-center gap-1.5 transition-colors"
          >
            <RefreshCw className="size-3.5" />
            Retry
          </button>
        </div>
      );
    }
    if (rootItems.length === 0) {
      return (
        <div className="flex-1 p-6 flex flex-col items-center justify-center text-center text-fg-3">
          <Folder className="size-7 text-fg-4 mb-2" />
          <div className="text-[13.5px] font-medium text-fg-2">Workspace is empty</div>
          <div className="mt-1 text-[12.5px] text-fg-4">Files created by {member.display_name} will appear here.</div>
        </div>
      );
    }
    return <div className="flex-1 overflow-y-auto scroll-thin p-2 space-y-0.5">{renderTree("", 0)}</div>;
  }

  const toolbar = (
    <div className="px-3 py-2 border-b border-line flex items-center gap-1.5 bg-subtle/30 shrink-0">
      <div
        className="flex-1 min-w-0 h-7 px-2 rounded-[7px] bg-card border border-line flex items-center gap-1.5 text-[11.5px] font-mono text-fg-3"
        title={rootPath || "Agent workspace"}
      >
        <Folder className="size-3.5 text-fg-4 shrink-0" />
        <span className="truncate direction-rtl">{rootPath || "workspace"}</span>
      </div>
      {rootPath && (
        <IconButton label={copiedPath ? "Copied path" : "Copy workspace path"} onClick={copyRootPath}>
          {copiedPath ? <Check className="text-work" /> : <Copy />}
        </IconButton>
      )}
      <IconButton
        label={includeHidden ? "Hide hidden files" : "Show hidden files"}
        active={includeHidden}
        onClick={() => setIncludeHidden((v) => !v)}
      >
        {includeHidden ? <Eye /> : <EyeOff />}
      </IconButton>
      <IconButton label="Refresh workspace" onClick={refresh}>
        <RefreshCw />
      </IconButton>
      {!fullscreen && (
        <IconButton label="Expand workspace" onClick={() => setFullscreen(true)}>
          <Maximize2 />
        </IconButton>
      )}
    </div>
  );

  return (
    <div className="flex-1 min-h-0 flex flex-col">
      {toolbar}
      {selectedFile ? renderFileViewer(true) : renderTreePane()}

      {fullscreen &&
        createPortal(
          <div
            role="dialog"
            aria-modal="true"
            aria-label={`${member.display_name} workspace`}
            className="fixed inset-0 z-50 bg-black/45 backdrop-blur-[2px] flex items-center justify-center p-6 animate-fade-in"
            onMouseDown={(e) => {
              if (e.target === e.currentTarget) setFullscreen(false);
            }}
          >
            <div className="w-full max-w-[1040px] h-[80vh] rounded-[14px] bg-panel border border-line shadow-lg flex flex-col overflow-hidden animate-pop-in">
              <div className="h-12 px-4 border-b border-line flex items-center justify-between gap-3 shrink-0">
                <div className="flex items-center gap-2 min-w-0">
                  <FolderOpen className="size-4 text-accent-text shrink-0" />
                  <span className="text-[14.5px] font-semibold text-fg truncate">
                    {member.display_name} · Workspace
                  </span>
                  {rootPath && (
                    <span className="hidden sm:inline font-mono text-[12px] text-fg-4 truncate max-w-[420px]" title={rootPath}>
                      {rootPath}
                    </span>
                  )}
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  <IconButton
                    label={includeHidden ? "Hide hidden files" : "Show hidden files"}
                    active={includeHidden}
                    onClick={() => setIncludeHidden((v) => !v)}
                  >
                    {includeHidden ? <Eye /> : <EyeOff />}
                  </IconButton>
                  <IconButton label="Refresh workspace" onClick={refresh}>
                    <RefreshCw />
                  </IconButton>
                  <IconButton label="Close" onClick={() => setFullscreen(false)}>
                    <X />
                  </IconButton>
                </div>
              </div>
              <div className="flex-1 min-h-0 flex divide-x divide-line">
                <div className="w-[280px] shrink-0 flex flex-col min-h-0 bg-panel">{renderTreePane()}</div>
                <div className="flex-1 min-w-0 flex flex-col min-h-0">{renderFileViewer(false)}</div>
              </div>
            </div>
          </div>,
          document.body,
        )}
    </div>
  );
}
