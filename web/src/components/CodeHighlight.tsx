import { isValidElement, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Check, Code2, Copy, GitBranch, ZoomIn, ZoomOut } from "lucide-react";

const LANG_ALIASES: Record<string, string> = {
  sh: "bash",
  shell: "bash",
  zsh: "bash",
  ts: "typescript",
  js: "javascript",
  py: "python",
  rs: "rust",
  yml: "yaml",
  md: "markdown",
  dockerfile: "docker",
  "c++": "cpp",
  "c#": "csharp",
  cs: "csharp",
  rb: "ruby",
  kt: "kotlin",
  gql: "graphql",
};

const LANG_LOADERS: Record<string, () => Promise<{ default: unknown }>> = {
  bash: () => import("@shikijs/langs/bash"),
  c: () => import("@shikijs/langs/c"),
  cpp: () => import("@shikijs/langs/cpp"),
  css: () => import("@shikijs/langs/css"),
  diff: () => import("@shikijs/langs/diff"),
  docker: () => import("@shikijs/langs/docker"),
  go: () => import("@shikijs/langs/go"),
  graphql: () => import("@shikijs/langs/graphql"),
  html: () => import("@shikijs/langs/html"),
  java: () => import("@shikijs/langs/java"),
  javascript: () => import("@shikijs/langs/javascript"),
  json: () => import("@shikijs/langs/json"),
  jsx: () => import("@shikijs/langs/jsx"),
  kotlin: () => import("@shikijs/langs/kotlin"),
  markdown: () => import("@shikijs/langs/markdown"),
  python: () => import("@shikijs/langs/python"),
  ruby: () => import("@shikijs/langs/ruby"),
  rust: () => import("@shikijs/langs/rust"),
  sql: () => import("@shikijs/langs/sql"),
  swift: () => import("@shikijs/langs/swift"),
  toml: () => import("@shikijs/langs/toml"),
  tsx: () => import("@shikijs/langs/tsx"),
  typescript: () => import("@shikijs/langs/typescript"),
  xml: () => import("@shikijs/langs/xml"),
  yaml: () => import("@shikijs/langs/yaml"),
};

export function normalizeCodeLanguage(raw?: string | null): string {
  if (!raw) return "";
  const lower = raw.trim().toLowerCase();
  return LANG_ALIASES[lower] ?? lower;
}

export function languageFromPath(path: string): string {
  const base = path.split("/").pop()?.toLowerCase() ?? "";
  if (base === "dockerfile") return "docker";
  const dot = base.lastIndexOf(".");
  if (dot < 0) return "";
  return normalizeCodeLanguage(base.slice(dot + 1));
}

export interface HighlightToken {
  content: string;
  color?: string;
}

type HighlightLines = HighlightToken[][];

const HIGHLIGHT_CACHE_MAX = 200;
const highlightCache = new Map<string, HighlightLines>();

function cacheGet(key: string): HighlightLines | undefined {
  const hit = highlightCache.get(key);
  if (hit) {
    highlightCache.delete(key);
    highlightCache.set(key, hit);
  }
  return hit;
}

function cacheSet(key: string, value: HighlightLines) {
  highlightCache.delete(key);
  highlightCache.set(key, value);
  while (highlightCache.size > HIGHLIGHT_CACHE_MAX) {
    const oldest = highlightCache.keys().next().value;
    if (!oldest) break;
    highlightCache.delete(oldest);
  }
}

interface HighlighterLike {
  getLoadedLanguages(): string[];
  loadLanguage(...langs: unknown[]): Promise<void>;
  codeToTokens(code: string, opts: { lang: string; theme: string }): { tokens: { content: string; color?: string }[][] };
}

let highlighterPromise: Promise<HighlighterLike> | null = null;
const loadingLangs = new Map<string, Promise<void>>();

async function getHighlighter(): Promise<HighlighterLike> {
  if (!highlighterPromise) {
    highlighterPromise = (async () => {
      const [{ createHighlighterCore }, { createJavaScriptRegexEngine }, githubLight, githubDark] = await Promise.all([
        import("shiki/core"),
        import("shiki/engine/javascript"),
        import("@shikijs/themes/github-light"),
        import("@shikijs/themes/github-dark"),
      ]);
      return (await createHighlighterCore({
        themes: [githubLight.default, githubDark.default],
        langs: [],
        engine: createJavaScriptRegexEngine(),
      })) as unknown as HighlighterLike;
    })();
  }
  return highlighterPromise;
}

async function highlightTokens(code: string, lang: string, dark: boolean): Promise<HighlightLines | null> {
  const norm = normalizeCodeLanguage(lang);
  const loader = LANG_LOADERS[norm];
  if (!loader || code.length > 100_000) return null;

  const theme = dark ? "github-dark" : "github-light";
  const key = `${theme}\0${norm}\0${code}`;
  const cached = cacheGet(key);
  if (cached) return cached;

  const hl = await getHighlighter();
  if (!hl.getLoadedLanguages().includes(norm)) {
    let pending = loadingLangs.get(norm);
    if (!pending) {
      pending = loader().then((mod) => hl.loadLanguage(mod.default));
      loadingLangs.set(norm, pending);
    }
    try {
      await pending;
    } finally {
      loadingLangs.delete(norm);
    }
  }
  if (!hl.getLoadedLanguages().includes(norm)) return null;
  const result = hl.codeToTokens(code, { lang: norm, theme });
  const lines: HighlightLines = result.tokens.map((line) => line.map((t) => ({ content: t.content, color: t.color })));
  cacheSet(key, lines);
  return lines;
}

function useIsDarkTheme(): boolean {
  const [dark, setDark] = useState(() => typeof document !== "undefined" && document.documentElement.classList.contains("dark"));
  useEffect(() => {
    if (typeof document === "undefined" || typeof MutationObserver === "undefined") return;
    const root = document.documentElement;
    const sync = () => setDark(root.classList.contains("dark"));
    sync();
    const obs = new MutationObserver(sync);
    obs.observe(root, { attributes: true, attributeFilter: ["class"] });
    return () => obs.disconnect();
  }, []);
  return dark;
}

function useHighlightedLines(code: string, lang: string, enabled: boolean): HighlightLines | null {
  const dark = useIsDarkTheme();
  const norm = normalizeCodeLanguage(lang);
  const cacheKey = `${dark ? "github-dark" : "github-light"}\0${norm}\0${code}`;
  const [state, setState] = useState<{ key: string; lines: HighlightLines } | null>(() => {
    const hit = enabled ? cacheGet(cacheKey) : undefined;
    return hit ? { key: cacheKey, lines: hit } : null;
  });

  useEffect(() => {
    if (!enabled || !LANG_LOADERS[norm]) return;
    const hit = cacheGet(cacheKey);
    if (hit) {
      setState({ key: cacheKey, lines: hit });
      return;
    }
    let cancelled = false;
    void highlightTokens(code, norm, dark)
      .then((lines) => {
        if (!cancelled && lines) setState({ key: cacheKey, lines });
      })
      .catch(() => {
        // Leave plain text fallback intact if grammar loading fails.
      });
    return () => {
      cancelled = true;
    };
  }, [cacheKey, code, norm, dark, enabled]);

  return state?.key === cacheKey ? state.lines : null;
}

export function extractText(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(extractText).join("");
  if (isValidElement<{ children?: ReactNode }>(node)) return extractText(node.props.children);
  return "";
}

export function extractLanguage(children?: ReactNode): string {
  if (!isValidElement<{ className?: string }>(children)) return "";
  const m = children.props.className?.match(/language-([A-Za-z0-9_+#-]+)/);
  return m ? m[1] : "";
}

/** Renders Mermaid source inside an empty-sandbox iframe so untrusted SVG never enters the main DOM. */
function MermaidBlock({ code, copyBtn }: { code: string; copyBtn: ReactNode }) {
  const dark = useIsDarkTheme();
  const [mode, setMode] = useState<"diagram" | "code">("diagram");
  const [zoom, setZoom] = useState(1);
  const [svg, setSvg] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (typeof window === "undefined") return;
    let cancelled = false;
    setFailed(false);
    setSvg(null);
    void import("mermaid")
      .then(async (mod) => {
        const mermaid = mod.default;
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          theme: dark ? "dark" : "default",
          fontFamily: "Inter, system-ui, sans-serif",
        });
        const id = `sumi-mmd-${Math.random().toString(36).slice(2, 10)}`;
        const rendered = await mermaid.render(id, code.trim());
        if (!cancelled) setSvg(rendered.svg);
      })
      .catch(() => {
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, [code, dark]);

  const srcDoc = useMemo(() => {
    if (!svg) return "";
    const bg = dark ? "#212630" : "#ffffff";
    return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:;"><style>html,body{margin:0;padding:12px;background:${bg};display:flex;justify-content:center;align-items:flex-start;overflow:auto;}svg{max-width:100%;height:auto;transform-origin:top center;transform:scale(${zoom});}</style></head><body>${svg}</body></html>`;
  }, [svg, dark, zoom]);

  const showDiagram = mode === "diagram" && !failed;

  return (
    <div className="my-2 rounded-[6px] border border-line bg-card overflow-hidden">
      <div className="h-8 px-2.5 border-b border-line bg-subtle/70 flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 text-[11.5px] font-mono text-fg-3">
          <GitBranch className="size-3.5 text-fg-4" />
          <span>mermaid</span>
        </div>
        <div className="flex items-center gap-1">
          {showDiagram && svg && (
            <>
              <button
                type="button"
                aria-label="Zoom out diagram"
                onClick={() => setZoom((z) => Math.max(0.5, Number((z - 0.2).toFixed(1))))}
                className="size-6 rounded-[4px] text-fg-3 hover:text-fg hover:bg-hover inline-flex items-center justify-center"
              >
                <ZoomOut className="size-3.5" />
              </button>
              <span className="tabular font-mono text-[11px] text-fg-4 w-9 text-center">{Math.round(zoom * 100)}%</span>
              <button
                type="button"
                aria-label="Zoom in diagram"
                onClick={() => setZoom((z) => Math.min(2.2, Number((z + 0.2).toFixed(1))))}
                className="size-6 rounded-[4px] text-fg-3 hover:text-fg hover:bg-hover inline-flex items-center justify-center"
              >
                <ZoomIn className="size-3.5" />
              </button>
            </>
          )}
          <div className="p-0.5 rounded-[4px] bg-subtle border border-line inline-flex items-center">
            <button
              type="button"
              onClick={() => setMode("diagram")}
              disabled={failed}
              className={`h-5 px-1.5 rounded-[3px] text-[11px] font-medium transition-colors disabled:opacity-40 ${
                showDiagram ? "bg-card text-fg" : "text-fg-3 hover:text-fg"
              }`}
            >
              Diagram
            </button>
            <button
              type="button"
              onClick={() => setMode("code")}
              className={`h-5 px-1.5 rounded-[3px] text-[11px] font-medium inline-flex items-center gap-1 transition-colors ${
                !showDiagram ? "bg-card text-fg" : "text-fg-3 hover:text-fg"
              }`}
            >
              <Code2 className="size-3" />
              Code
            </button>
          </div>
          {copyBtn}
        </div>
      </div>
      {showDiagram ? (
        svg ? (
          <iframe
            title="Mermaid diagram"
            sandbox=""
            srcDoc={srcDoc}
            className="w-full h-[280px] border-0 block bg-card"
          />
        ) : (
          <div className="px-3.5 py-4 text-[12.5px] text-fg-3 font-mono">Rendering diagram…</div>
        )
      ) : (
        <pre className="!my-0 !border-0 !rounded-none !bg-transparent p-3 text-[13px] leading-[1.55] font-mono overflow-x-auto">
          <code>{code}</code>
        </pre>
      )}
    </div>
  );
}

/**
 * Shared syntax-highlighted code block used by chat messages, workspace file previews,
 * and document attachment previews. Controls sit outside the code area so line 1 is never obscured.
 */
export function CodeBlock({
  code,
  language,
  children,
  showLineNumbers = false,
  bare = false,
  className = "",
}: {
  code?: string;
  language?: string;
  children?: ReactNode;
  showLineNumbers?: boolean;
  bare?: boolean;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);
  const rawCode = code ?? extractText(children);
  const rawLang = language ?? extractLanguage(children);
  const normLang = normalizeCodeLanguage(rawLang);
  const displayCode = rawCode.endsWith("\n") ? rawCode.slice(0, -1) : rawCode;
  const isMermaid = normLang === "mermaid" && !showLineNumbers && !bare;
  const highlighted = useHighlightedLines(displayCode, normLang, !isMermaid);

  function copy() {
    if (!rawCode) return;
    void navigator.clipboard?.writeText(rawCode).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    });
  }

  const copyBtn = (
    <button
      type="button"
      onClick={copy}
      aria-label={copied ? "Copied code" : "Copy code"}
      title={copied ? "Copied" : "Copy"}
      className="h-6 px-2 rounded-[5px] text-[11.5px] text-fg-3 hover:text-fg hover:bg-hover inline-flex items-center gap-1 transition-colors shrink-0"
    >
      {copied ? <Check className="size-3.5 text-work" /> : <Copy className="size-3.5" />}
      <span>{copied ? "Copied" : "Copy"}</span>
    </button>
  );

  if (isMermaid) {
    return <MermaidBlock code={rawCode} copyBtn={copyBtn} />;
  }

  const plainLines = useMemo(() => displayCode.split("\n"), [displayCode]);

  const codeBody = highlighted ? (
    <code className="block font-mono">
      {highlighted.map((tokens, i) => (
        <span key={i} className="block min-h-[1.55em]">
          {showLineNumbers && (
            <span className="inline-block w-10 pr-3 text-right select-none text-fg-4 tabular">{i + 1}</span>
          )}
          {tokens.length > 0 ? (
            tokens.map((t, j) => (
              <span key={j} style={t.color ? { color: t.color } : undefined}>
                {t.content}
              </span>
            ))
          ) : (
            "\n"
          )}
        </span>
      ))}
    </code>
  ) : showLineNumbers ? (
    <code className="block font-mono">
      {plainLines.map((line, i) => (
        <span key={i} className="block min-h-[1.55em]">
          <span className="inline-block w-10 pr-3 text-right select-none text-fg-4 tabular">{i + 1}</span>
          {line || "\n"}
        </span>
      ))}
    </code>
  ) : (
    <code>{rawCode}</code>
  );

  if (bare) {
    return (
      <pre className={`p-3.5 text-[12.5px] leading-[1.6] font-mono text-fg whitespace-pre overflow-x-auto ${className}`}>
        {codeBody}
      </pre>
    );
  }

  if (rawLang) {
    return (
      <div className={`my-2 rounded-[6px] border border-line bg-subtle overflow-hidden ${className}`}>
        <div className="h-8 px-3 border-b border-line bg-card/50 flex items-center justify-between gap-2">
          <span className="font-mono text-[11.5px] text-fg-3 select-none truncate">{rawLang}</span>
          {copyBtn}
        </div>
        <pre className="!m-0 !border-0 !rounded-none !bg-transparent px-3.5 py-2.5 overflow-x-auto text-[13px] leading-[1.55] font-mono scroll-thin">
          {codeBody}
        </pre>
      </div>
    );
  }

  // Unlabelled fenced block: reserve a dedicated right column for Copy so it never overlaps line 1.
  return (
    <div className={`group/code my-2 rounded-[6px] border border-line bg-subtle flex items-start overflow-hidden ${className}`}>
      <pre className="!m-0 !border-0 !rounded-none !bg-transparent flex-1 min-w-0 px-3.5 py-2.5 overflow-x-auto text-[13px] leading-[1.55] font-mono scroll-thin">
        {codeBody}
      </pre>
      <div className="p-1.5 shrink-0 opacity-0 group-hover/code:opacity-100 focus-within:opacity-100 transition-opacity">
        {copyBtn}
      </div>
    </div>
  );
}
