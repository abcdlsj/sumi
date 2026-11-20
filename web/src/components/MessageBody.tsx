import { useMemo, useRef } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import type { Member } from "../api";

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
 * Remark plugin that turns `@display_name` (and `@all`) inside plain text nodes into
 * mention spans while leaving code blocks, inline code, links and unknown handles alone.
 */
function createRemarkMentions(names: string[]) {
  const unique = Array.from(new Set(["all", ...names.map((n) => n.toLowerCase())])).sort((a, b) => b.length - a.length);
  const pattern = new RegExp(`(^|[^A-Za-z0-9_])@(${unique.map(escapeRegex).join("|")})(?![A-Za-z0-9_])`, "giu");

  function transformText(value: string): MdastNode[] | null {
    pattern.lastIndex = 0;
    let match = pattern.exec(value);
    if (!match) return null;

    const out: MdastNode[] = [];
    let cursor = 0;
    while (match) {
      const prefix = match[1];
      const name = match[2];
      const atIndex = match.index + prefix.length;
      if (atIndex > cursor) {
        out.push({ type: "text", value: value.slice(cursor, atIndex) });
      }
      out.push({
        type: "sumiMention",
        data: {
          hName: "span",
          hProperties: { "data-mention": name.toLowerCase() },
        },
        children: [{ type: "text", value: `@${name}` }],
      });
      cursor = atIndex + 1 + name.length;
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

export function MessageBody({
  body,
  members,
  selfID,
  onSelectMember,
}: {
  body: string;
  members: Member[];
  selfID?: string;
  onSelectMember?: (member: Member) => void;
}) {
  const memberByKey = useMemo(() => {
    const map = new Map<string, Member>();
    for (const m of members) map.set(m.display_name.toLowerCase(), m);
    return map;
  }, [members]);

  const plugins = useMemo(() => {
    const names = members.map((m) => m.display_name);
    return [remarkGfm, createRemarkMentions(names)];
  }, [members]);

  const onSelectRef = useRef(onSelectMember);
  onSelectRef.current = onSelectMember;
  const clickable = Boolean(onSelectMember);

  const components = useMemo<Components>(
    () => ({
      span({ node: _node, children, ...props }) {
        const key = (props as Record<string, unknown>)["data-mention"];
        if (typeof key !== "string") return <span {...props}>{children}</span>;

        if (key === "all") {
          return (
            <span
              data-mention="all"
              className="inline-flex items-baseline px-1 py-[0.5px] -my-[0.5px] rounded-[5px] font-medium bg-accent-soft text-accent-text ring-1 ring-inset ring-accent/30"
            >
              {children}
            </span>
          );
        }

        const member = memberByKey.get(key);
        if (!member) return <span {...props}>{children}</span>;

        const isSelf = member.id === selfID;
        const base =
          "inline-flex items-baseline px-1 py-[0.5px] -my-[0.5px] rounded-[5px] font-medium bg-accent-soft text-accent-text transition-colors";
        const emphasis = isSelf ? " ring-1 ring-inset ring-accent/30" : "";

        if (!clickable) {
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
            onClick={() => onSelectRef.current?.(member)}
            className={`${base}${emphasis} hover:underline underline-offset-2`}
          >
            {children}
          </button>
        );
      },
    }),
    [memberByKey, selfID, clickable],
  );

  return (
    <ReactMarkdown remarkPlugins={plugins} components={components}>
      {body}
    </ReactMarkdown>
  );
}
