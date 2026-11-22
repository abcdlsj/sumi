import { Component, memo, useMemo, useRef, type ReactNode } from "react";
import { FileText } from "lucide-react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkCjkFriendly from "remark-cjk-friendly";
import remarkGfm from "remark-gfm";
import type { BootstrapData, Member, Task } from "../api";
import { MemberHoverCard } from "./AgentVisibility";
import { CodeBlock } from "./CodeHighlight";

const WORKSPACE_FILE_EXTS = new Set([
  "md", "markdown", "mdx", "txt", "rst", "adoc",
  "json", "jsonc", "jsonl", "yaml", "yml", "toml", "xml", "csv", "tsv",
  "sql", "graphql", "gql", "prisma", "ini", "conf", "cfg", "properties", "log", "diff", "patch",
  "ts", "tsx", "js", "jsx", "mjs", "cjs", "mts", "cts",
  "go", "mod", "sum", "py", "pyi", "rs", "rb", "php", "java", "kt", "kts", "swift",
  "c", "cc", "cpp", "cxx", "h", "hpp", "cs",
  "sh", "bash", "zsh", "fish", "ps1",
  "css", "scss", "sass", "less", "html", "htm", "svg", "vue", "svelte", "astro",
  "proto", "tf", "hcl", "mk", "lua", "r", "dart", "ex", "exs", "erl", "zig", "nim",
  "png", "jpg", "jpeg", "gif", "webp", "avif", "pdf",
]);

const KNOWN_FILENAMES = new Set([
  "makefile", "dockerfile", "readme", "license", "changelog",
  "procfile", "justfile", "taskfile", "gemfile", "rakefile", "vagrantfile",
  ".gitignore", ".dockerignore", ".editorconfig",
]);

const BLOCKED_DIRS = new Set([".aws", ".gnupg", ".ssh", ".sumi"]);

/**
 * Extracts a clean workspace-relative file path from an agent's inline code, link href, or prose token.
 * Rejects URLs, traversal, shell commands, arbitrary absolute paths, and secret filenames.
 */
export function parseWorkspaceFilePath(raw: string): string | null {
  let s = raw.trim();
  if (!s || s.length > 260) return null;
  if (/^[a-z][a-z0-9+.-]*:/i.test(s) || s.startsWith("//")) return null;

  // Strip optional line/column anchors like :42, :42:5, :10-20, #L12, #L12-L20.
  s = s.replace(/(?::\d+(?::\d+)?(?:-\d+(?::\d+)?)?|#L\d+(?:C\d+)?(?:-L?\d+(?:C\d+)?)?)$/i, "");
  if (!s) return null;

  // Allow absolute paths only when they point inside an agent's .../workspace/ directory.
  if (s.startsWith("/")) {
    const wsMatch = s.match(/^\/.+\/workspace\/(.+)$/);
    if (!wsMatch) return null;
    s = wsMatch[1];
  }

  while (s.startsWith("./")) s = s.slice(2);
  if (!s || s.startsWith("/") || s.startsWith("~") || s.startsWith("-") || s.endsWith("/")) return null;
  if (/[\s\\*?<>|"'()[\]{};&$!=,\0]/.test(s)) return null;

  const segments = s.split("/");
  const clean: string[] = [];
  for (const seg of segments) {
    if (!seg || seg === "." || seg === "..") return null;
    if (BLOCKED_DIRS.has(seg.toLowerCase())) return null;
    clean.push(seg);
  }
  const base = clean[clean.length - 1];
  const lowerBase = base.toLowerCase();
  if (lowerBase === ".env" || lowerBase.startsWith(".env.") || lowerBase.startsWith("id_rsa") || lowerBase.startsWith("id_ed25519")) {
    return null;
  }
  // Avoid matching common JS expressions like `console.log` when bare.
  if (clean.length === 1 && lowerBase === "console.log") return null;

  if (KNOWN_FILENAMES.has(lowerBase)) return clean.join("/");
  const dot = base.lastIndexOf(".");
  if (dot <= 0 || dot === base.length - 1) return null;
  const ext = base.slice(dot + 1).toLowerCase();
  if (!WORKSPACE_FILE_EXTS.has(ext)) return null;
  return clean.join("/");
}

class MarkdownErrorBoundary extends Component<{ body: string; children: ReactNode }, { body: string; failed: boolean }> {
  state = { body: this.props.body, failed: false };
  static getDerivedStateFromProps(props: { body: string }, state: { body: string; failed: boolean }) {
    return props.body !== state.body ? { body: props.body, failed: false } : null;
  }
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    if (this.state.failed) {
      return <pre className="whitespace-pre-wrap break-words font-mono text-[13px] text-fg-2">{this.props.body}</pre>;
    }
    return this.props.children;
  }
}

interface MdastNode {
  type: string;
  value?: string;
  url?: string;
  children?: MdastNode[];
  data?: {
    hName?: string;
    hProperties?: Record<string, string>;
  };
}

const SKIP_TYPES = new Set(["code", "inlineCode", "link", "linkReference", "html", "definition"]);

// Matches plain-text file paths like docs/plan.md, ./notes.md, or src/app.ts:42 without eating URLs or emails.
const PLAIN_FILE_RE = /(^|[^A-Za-z0-9_@:/])((?:\.\/)?(?:[A-Za-z0-9._-]+\/)*[A-Za-z0-9_-]+\.[A-Za-z0-9]{1,10}(?::\d+(?::\d+)?(?:-\d+(?::\d+)?)?|#L\d+(?:C\d+)?(?:-L?\d+(?:C\d+)?)?)?)(?![A-Za-z0-9_/@])/giu;

function escapeRegex(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Remark plugin that turns `@display_name` (plus `@all`) and `#<task_number>` inside plain text
 * nodes into interactive chips, and optionally turns workspace file paths in agent messages
 * (inline code, relative links, and plain text) into clickable workspace file links.
 */
function createRemarkRefs(names: string[], taskNumbers: number[], detectFiles: boolean) {
  const uniqueNames = Array.from(new Set(["all", ...names.map((n) => n.toLowerCase())])).sort((a, b) => b.length - a.length);
  const nameAlt = uniqueNames.map(escapeRegex).join("|");
  const numAlt = taskNumbers.length > 0 ? Array.from(new Set(taskNumbers)).sort((a, b) => b - a).join("|") : "";

  // Group 1: boundary prefix; Group 2: @mention name; Group 3: #task number.
  const branches = [`@(${nameAlt})(?![A-Za-z0-9_])`];
  if (numAlt) branches.push(`#(${numAlt})(?![0-9A-Za-z_])`);
  const pattern = new RegExp(`(^|[^A-Za-z0-9_#])(?:${branches.join("|")})`, "giu");

  function transformPlainFiles(value: string): MdastNode[] | null {
    PLAIN_FILE_RE.lastIndex = 0;
    let match = PLAIN_FILE_RE.exec(value);
    if (!match) return null;

    const out: MdastNode[] = [];
    let cursor = 0;
    let changed = false;
    while (match) {
      const prefix = match[1];
      const token = match[2];
      const start = match.index + prefix.length;
      const resolved = parseWorkspaceFilePath(token);
      if (resolved) {
        if (start > cursor) {
          out.push({ type: "text", value: value.slice(cursor, start) });
        }
        out.push({
          type: "sumiFileRef",
          data: {
            hName: "span",
            hProperties: { "data-workspace-file": resolved },
          },
          children: [{ type: "text", value: token }],
        });
        cursor = start + token.length;
        changed = true;
      }
      match = PLAIN_FILE_RE.exec(value);
    }
    if (!changed) return null;
    if (cursor < value.length) {
      out.push({ type: "text", value: value.slice(cursor) });
    }
    return out;
  }

  function transformText(value: string): MdastNode[] | null {
    pattern.lastIndex = 0;
    let match = pattern.exec(value);
    if (!match) {
      return detectFiles ? transformPlainFiles(value) : null;
    }

    const out: MdastNode[] = [];
    let cursor = 0;
    while (match) {
      const prefix = match[1];
      const mention = match[2];
      const taskNum = match[3];
      const start = match.index + prefix.length;
      if (start > cursor) {
        const chunk = value.slice(cursor, start);
        const fileParts = detectFiles ? transformPlainFiles(chunk) : null;
        if (fileParts) out.push(...fileParts);
        else out.push({ type: "text", value: chunk });
      }
      if (mention) {
        out.push({
          type: "sumiMention",
          data: {
            hName: "span",
            hProperties: { "data-mention": mention.toLowerCase() },
          },
          children: [{ type: "text", value: `@${mention}` }],
        });
        cursor = start + 1 + mention.length;
      } else if (taskNum) {
        out.push({
          type: "sumiTaskRef",
          data: {
            hName: "span",
            hProperties: { "data-task-ref": taskNum },
          },
          children: [{ type: "text", value: `#${taskNum}` }],
        });
        cursor = start + 1 + taskNum.length;
      }
      match = pattern.exec(value);
    }
    if (cursor < value.length) {
      const tail = value.slice(cursor);
      const fileParts = detectFiles ? transformPlainFiles(tail) : null;
      if (fileParts) out.push(...fileParts);
      else out.push({ type: "text", value: tail });
    }
    return out;
  }

  function walk(node: MdastNode) {
    if (!node.children || SKIP_TYPES.has(node.type)) return;
    const next: MdastNode[] = [];
    let changed = false;
    for (const child of node.children) {
      if (child.type === "text" && typeof child.value === "string") {
        const parts = transformText(child.value);
        if (parts) {
          next.push(...parts);
          changed = true;
          continue;
        }
      } else if (detectFiles && child.type === "inlineCode" && typeof child.value === "string") {
        const resolved = parseWorkspaceFilePath(child.value);
        if (resolved) {
          next.push({
            type: "sumiFileRef",
            data: {
              hName: "span",
              hProperties: { "data-workspace-file": resolved },
            },
            children: [{ type: "text", value: child.value }],
          });
          changed = true;
          continue;
        }
      } else if (detectFiles && child.type === "link" && typeof child.url === "string") {
        const resolved = parseWorkspaceFilePath(child.url);
        if (resolved) {
          child.data = {
            ...child.data,
            hProperties: {
              ...(child.data?.hProperties ?? {}),
              "data-workspace-file": resolved,
            },
          };
        }
      } else {
        walk(child);
      }
      next.push(child);
    }
    if (changed) node.children = next;
  }

  return () => (tree: MdastNode) => {
    walk(tree);
  };
}

export const MessageBody = memo(function MessageBody({
  body,
  members,
  tasks = [],
  boot,
  selfID,
  onSelectMember,
  onSelectTask,
  onOpenWorkspaceFile,
}: {
  body: string;
  members: Member[];
  tasks?: Task[];
  boot?: BootstrapData;
  selfID?: string;
  onSelectMember?: (member: Member) => void;
  onSelectTask?: (task: Task) => void;
  onOpenWorkspaceFile?: (filePath: string) => void;
}) {
  const membersRef = useRef(members);
  membersRef.current = members;
  const tasksRef = useRef(tasks);
  tasksRef.current = tasks;
  const bootRef = useRef(boot);
  bootRef.current = boot;

  // Key by names and task numbers only so agent state ticks never re-parse markdown.
  const memberSig = members.map((m) => `${m.id}:${m.display_name}:${m.kind}`).join("\0");
  const taskSig = tasks.map((t) => `${t.id}:${t.number}:${t.title}`).join("\0");
  const clickableFile = Boolean(onOpenWorkspaceFile);

  const memberByKey = useMemo(() => {
    const map = new Map<string, Member>();
    for (const m of membersRef.current) map.set(m.display_name.toLowerCase(), m);
    return map;
  }, [memberSig]);

  const taskByNum = useMemo(() => {
    const map = new Map<string, Task>();
    for (const t of tasksRef.current) map.set(String(t.number), t);
    return map;
  }, [taskSig]);

  const plugins = useMemo(() => {
    const names = membersRef.current.map((m) => m.display_name);
    const nums = tasksRef.current.map((t) => t.number);
    return [remarkGfm, remarkCjkFriendly, createRemarkRefs(names, nums, clickableFile)];
  }, [memberSig, taskSig, clickableFile]);

  const onSelectMemberRef = useRef(onSelectMember);
  onSelectMemberRef.current = onSelectMember;
  const onSelectTaskRef = useRef(onSelectTask);
  onSelectTaskRef.current = onSelectTask;
  const onOpenWorkspaceFileRef = useRef(onOpenWorkspaceFile);
  onOpenWorkspaceFileRef.current = onOpenWorkspaceFile;
  const clickableMember = Boolean(onSelectMember);
  const clickableTask = Boolean(onSelectTask);

  const components = useMemo<Components>(
    () => ({
      pre({ children }) {
        return <CodeBlock>{children}</CodeBlock>;
      },
      table({ children }) {
        return (
          <div className="my-2 overflow-x-auto rounded-[7px] border border-line bg-card/50">
            <table>{children}</table>
          </div>
        );
      },
      a({ node: _node, children, href, ...props }) {
        const record = props as Record<string, unknown>;
        const wsFile = record["data-workspace-file"];
        if (typeof wsFile === "string" && clickableFile) {
          return (
            <a
              {...props}
              href={href}
              title={`Open ${wsFile} in workspace`}
              onClick={(e) => {
                e.preventDefault();
                onOpenWorkspaceFileRef.current?.(wsFile);
              }}
            >
              {children}
            </a>
          );
        }
        return (
          <a {...props} href={href} target="_blank" rel="noreferrer">
            {children}
          </a>
        );
      },
      span({ node: _node, children, ...props }) {
        const record = props as Record<string, unknown>;
        const wsFile = record["data-workspace-file"];
        if (typeof wsFile === "string" && clickableFile) {
          return (
            <button
              type="button"
              data-workspace-file={wsFile}
              title={`Open ${wsFile} in workspace`}
              onClick={() => onOpenWorkspaceFileRef.current?.(wsFile)}
              className="inline-flex items-baseline gap-1 px-1.5 py-[0.5px] -my-[0.5px] rounded-[4px] font-mono text-[0.88em] bg-subtle text-fg border border-line hover:border-accent hover:text-accent-text transition-colors cursor-pointer"
            >
              <FileText className="size-3 text-fg-3 self-center shrink-0" aria-hidden />
              <span>{children}</span>
            </button>
          );
        }

        const taskKey = record["data-task-ref"];
        if (typeof taskKey === "string") {
          const task = taskByNum.get(taskKey);
          if (!task) return <span {...props}>{children}</span>;
          const cls =
            "inline-flex items-baseline px-1.5 py-[0.5px] -my-[0.5px] rounded-[4px] font-mono text-[0.88em] font-medium bg-subtle text-fg border border-line transition-colors";
          if (!clickableTask) {
            return (
              <span data-task-ref={task.number} className={cls}>
                {children}
              </span>
            );
          }
          return (
            <button
              type="button"
              data-task-ref={task.number}
              title={`#${task.number} ${task.title}`}
              onClick={() => onSelectTaskRef.current?.(task)}
              className={`${cls} hover:border-accent hover:text-accent-text`}
            >
              {children}
            </button>
          );
        }

        const key = record["data-mention"];
        if (typeof key !== "string") return <span {...props}>{children}</span>;

        if (key === "all") {
          return (
            <span
              data-mention="all"
              className="inline-flex items-baseline px-[5px] py-[0.5px] -my-[0.5px] rounded-[4px] text-[0.9em] leading-[1.35] font-medium bg-accent-soft/65 text-fg ring-1 ring-inset ring-accent/20"
            >
              {children}
            </span>
          );
        }

        const member = memberByKey.get(key);
        if (!member) return <span {...props}>{children}</span>;

        const isSelf = member.id === selfID;
        const base = isSelf
          ? "inline-flex items-baseline px-[5px] py-[0.5px] -my-[0.5px] rounded-[4px] text-[0.9em] leading-[1.35] font-medium bg-accent-soft/65 text-fg ring-1 ring-inset ring-accent/20 transition-colors"
          : "inline-flex items-baseline px-[5px] py-[0.5px] -my-[0.5px] rounded-[4px] text-[0.9em] leading-[1.35] font-medium bg-subtle text-fg-2 transition-colors";
        const hover = isSelf
          ? " hover:bg-accent-soft hover:underline underline-offset-2"
          : " hover:bg-hover hover:text-fg hover:underline underline-offset-2";

        if (!clickableMember) {
          return (
            <span data-mention={member.display_name} className={base}>
              {children}
            </span>
          );
        }

        const btn = (
          <button
            type="button"
            data-mention={member.display_name}
            title={member.kind === "agent" ? `Inspect agent ${member.display_name}` : `View ${member.display_name}`}
            onClick={() => onSelectMemberRef.current?.(member)}
            className={`${base}${hover}`}
          >
            {children}
          </button>
        );
        if (bootRef.current) {
          return (
            <MemberHoverCard boot={bootRef.current} member={member} onInspect={(m) => onSelectMemberRef.current?.(m)}>
              {btn}
            </MemberHoverCard>
          );
        }
        return btn;
      },
    }),
    [memberByKey, taskByNum, selfID, clickableMember, clickableTask, clickableFile],
  );

  return (
    <MarkdownErrorBoundary body={body}>
      <ReactMarkdown remarkPlugins={plugins} components={components}>
        {body}
      </ReactMarkdown>
    </MarkdownErrorBoundary>
  );
});
