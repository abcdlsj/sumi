import { isValidElement, useMemo, useRef, useState, type ReactNode } from "react";
import { Check, Copy } from "lucide-react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import type { Member, Task } from "../api";

interface MdastNode {
  type: string;
  value?: string;
  children?: MdastNode[];
  data?: {
    hName?: string;
    hProperties?: Record<string, string>;
  };
}

const SKIP_TYPES = new Set(["code", "inlineCode", "link", "linkReference", "html", "definition"]);

function escapeRegex(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Remark plugin that turns `@display_name` (plus `@all`) and `#<task_number>` inside plain text
 * nodes into interactive chips while leaving code blocks, inline code, links and unknown tokens alone.
 */
function createRemarkRefs(names: string[], taskNumbers: number[]) {
  const uniqueNames = Array.from(new Set(["all", ...names.map((n) => n.toLowerCase())])).sort((a, b) => b.length - a.length);
  const nameAlt = uniqueNames.map(escapeRegex).join("|");
  const numAlt = taskNumbers.length > 0 ? Array.from(new Set(taskNumbers)).sort((a, b) => b - a).join("|") : "";

  // Group 1: boundary prefix; Group 2: @mention name; Group 3: #task number.
  const branches = [`@(${nameAlt})(?![A-Za-z0-9_])`];
  if (numAlt) branches.push(`#(${numAlt})(?![0-9A-Za-z_])`);
  const pattern = new RegExp(`(^|[^A-Za-z0-9_#])(?:${branches.join("|")})`, "giu");

  function transformText(value: string): MdastNode[] | null {
    pattern.lastIndex = 0;
    let match = pattern.exec(value);
    if (!match) return null;

    const out: MdastNode[] = [];
    let cursor = 0;
    while (match) {
      const prefix = match[1];
      const mention = match[2];
      const taskNum = match[3];
      const start = match.index + prefix.length;
      if (start > cursor) {
        out.push({ type: "text", value: value.slice(cursor, start) });
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
      out.push({ type: "text", value: value.slice(cursor) });
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

function codeLanguage(children?: ReactNode): string {
  if (!isValidElement<{ className?: string }>(children)) return "";
  const m = children.props.className?.match(/language-([A-Za-z0-9_+-]+)/);
  return m ? m[1] : "";
}

function CodeBlock({ children }: { children?: ReactNode }) {
  const preRef = useRef<HTMLPreElement | null>(null);
  const [copied, setCopied] = useState(false);
  const lang = codeLanguage(children);

  function copy() {
    const text = preRef.current?.textContent ?? "";
    if (!text) return;
    void navigator.clipboard?.writeText(text).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    });
  }

  return (
    <div className="relative group/code my-2">
      <pre ref={preRef}>{children}</pre>
      <div className="absolute top-2 right-2 flex items-center gap-1.5">
        {lang && (
          <span className="h-6 px-1.5 rounded-[6px] bg-card/80 border border-line font-mono text-[11px] text-fg-4 inline-flex items-center select-none">
            {lang}
          </span>
        )}
        <button
          type="button"
          onClick={copy}
          aria-label={copied ? "Copied code" : "Copy code"}
          title={copied ? "Copied" : "Copy"}
          className="h-7 px-2 rounded-[7px] bg-card/90 border border-line text-[12px] text-fg-3 hover:text-fg hover:bg-hover inline-flex items-center gap-1 opacity-0 group-hover/code:opacity-100 focus-visible:opacity-100 transition-opacity shadow-sm"
        >
          {copied ? <Check className="size-3.5 text-work" /> : <Copy className="size-3.5" />}
          <span>{copied ? "Copied" : "Copy"}</span>
        </button>
      </div>
    </div>
  );
}

export function MessageBody({
  body,
  members,
  tasks = [],
  selfID,
  onSelectMember,
  onSelectTask,
}: {
  body: string;
  members: Member[];
  tasks?: Task[];
  selfID?: string;
  onSelectMember?: (member: Member) => void;
  onSelectTask?: (task: Task) => void;
}) {
  const memberByKey = useMemo(() => {
    const map = new Map<string, Member>();
    for (const m of members) map.set(m.display_name.toLowerCase(), m);
    return map;
  }, [members]);

  const taskByNum = useMemo(() => {
    const map = new Map<string, Task>();
    for (const t of tasks) map.set(String(t.number), t);
    return map;
  }, [tasks]);

  const plugins = useMemo(() => {
    const names = members.map((m) => m.display_name);
    const nums = tasks.map((t) => t.number);
    return [remarkGfm, createRemarkRefs(names, nums)];
  }, [members, tasks]);

  const onSelectMemberRef = useRef(onSelectMember);
  onSelectMemberRef.current = onSelectMember;
  const onSelectTaskRef = useRef(onSelectTask);
  onSelectTaskRef.current = onSelectTask;
  const clickableMember = Boolean(onSelectMember);
  const clickableTask = Boolean(onSelectTask);

  const components = useMemo<Components>(
    () => ({
      pre({ children }) {
        return <CodeBlock>{children}</CodeBlock>;
      },
      table({ children }) {
        return (
          <div className="my-2 overflow-x-auto rounded-[9px] border border-line bg-card/50">
            <table>{children}</table>
          </div>
        );
      },
      span({ node: _node, children, ...props }) {
        const record = props as Record<string, unknown>;
        const taskKey = record["data-task-ref"];
        if (typeof taskKey === "string") {
          const task = taskByNum.get(taskKey);
          if (!task) return <span {...props}>{children}</span>;
          const cls =
            "inline-flex items-baseline px-1.5 py-[0.5px] -my-[0.5px] rounded-[5px] font-mono text-[0.88em] font-medium bg-subtle text-fg border border-line transition-colors";
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
              className="inline-flex items-baseline px-[5px] py-[0.5px] -my-[0.5px] rounded-[5px] text-[0.9em] leading-[1.35] font-medium bg-accent-soft text-accent-text ring-1 ring-inset ring-accent/30"
            >
              {children}
            </span>
          );
        }

        const member = memberByKey.get(key);
        if (!member) return <span {...props}>{children}</span>;

        const isSelf = member.id === selfID;
        const base =
          "inline-flex items-baseline px-[5px] py-[0.5px] -my-[0.5px] rounded-[5px] text-[0.9em] leading-[1.35] font-medium bg-accent-soft text-accent-text transition-colors";
        const emphasis = isSelf ? " ring-1 ring-inset ring-accent/30" : "";

        if (!clickableMember) {
          return (
            <span data-mention={member.display_name} className={base + emphasis}>
              {children}
            </span>
          );
        }

        return (
          <button
            type="button"
            data-mention={member.display_name}
            title={member.kind === "agent" ? `Inspect agent ${member.display_name}` : `View ${member.display_name}`}
            onClick={() => onSelectMemberRef.current?.(member)}
            className={`${base}${emphasis} hover:underline underline-offset-2`}
          >
            {children}
          </button>
        );
      },
    }),
    [memberByKey, taskByNum, selfID, clickableMember, clickableTask],
  );

  return (
    <ReactMarkdown remarkPlugins={plugins} components={components}>
      {body}
    </ReactMarkdown>
  );
}
