package pi

import (
	"bytes"
	"os"
	"path/filepath"
)

// compactionFitExtensionSource is a zero-dependency Pi extension loaded via `--extension`.
// It intercepts `session_before_compact` and trims oversized history in place (copying
// message objects without mutating Pi's session store) so Pi's single summarization request
// always fits inside the model's context limit.
const compactionFitExtensionSource = `// Managed by Sumi: trims oversized Pi compaction requests before summarization runs.

const SAFETY_FACTOR = 0.85;
const OVERFLOW_WINDOW_FACTOR = 0.8;
const SUMMARY_PROMPT_TOKENS = 2000;
const MIN_BUDGET_TOKENS = 2000;
const CHARS_PER_TOKEN = 3.5;
const MIN_CUT_CHARS = 256;
const IMAGE_TOKEN_ESTIMATE = 1600;
const MESSAGE_OVERHEAD_TOKENS = 16;
const OMITTED_NOTE_TOKENS = 60;

const CJK_RE = /[\u3040-\u30FF\u3400-\u4DBF\u4E00-\u9FFF\uAC00-\uD7AF\uF900-\uFAFF\uFF00-\uFFEF]/gu;

export function estimatePromptTokens(text) {
  if (!text) return 0;
  const cjk = text.match(CJK_RE)?.length ?? 0;
  return cjk + Math.ceil((text.length - cjk) / CHARS_PER_TOKEN);
}

function estimateMessageTokens(message) {
  if (!message || typeof message !== "object") return MESSAGE_OVERHEAD_TOKENS;
  const content = message.content;
  if (typeof content === "string") {
    return MESSAGE_OVERHEAD_TOKENS + estimatePromptTokens(content);
  }
  if (!Array.isArray(content)) {
    try {
      return MESSAGE_OVERHEAD_TOKENS + estimatePromptTokens(JSON.stringify(message));
    } catch {
      return MESSAGE_OVERHEAD_TOKENS;
    }
  }
  let tokens = MESSAGE_OVERHEAD_TOKENS;
  for (const block of content) {
    if (!block || typeof block !== "object") continue;
    if (block.type === "text" && typeof block.text === "string") {
      tokens += estimatePromptTokens(block.text);
    } else if (block.type === "thinking" && typeof block.thinking === "string") {
      tokens += estimatePromptTokens(block.thinking);
    } else if (block.type === "toolCall") {
      tokens += estimatePromptTokens(String(block.name ?? ""));
      if (block.arguments !== undefined) {
        try {
          tokens += estimatePromptTokens(JSON.stringify(block.arguments));
        } catch {
          tokens += 64;
        }
      }
    } else if (block.type === "image") {
      tokens += IMAGE_TOKEN_ESTIMATE;
    } else {
      try {
        tokens += estimatePromptTokens(JSON.stringify(block));
      } catch {
        tokens += 32;
      }
    }
  }
  return tokens;
}

export function piCompactionFitBudget(preparation, model, trigger) {
  if (!preparation || typeof preparation !== "object") return null;
  const configured = typeof model?.contextWindow === "number" && model.contextWindow > 0 ? model.contextWindow : null;
  const rejected = trigger === "overflow" && typeof preparation.tokensBefore === "number" && preparation.tokensBefore > 0
    ? Math.floor(preparation.tokensBefore * OVERFLOW_WINDOW_FACTOR)
    : null;
  const win = configured === null ? rejected : rejected === null ? configured : Math.min(configured, rejected);
  if (win === null) return null;
  const reserve = Math.max(0, preparation.settings?.reserveTokens ?? 0);
  const maxTokens = typeof model?.maxTokens === "number" && model.maxTokens > 0 ? model.maxTokens : Number.POSITIVE_INFINITY;
  const output = Math.min(Math.floor(0.8 * reserve), maxTokens);
  const budget = Math.floor(win * SAFETY_FACTOR) - output - SUMMARY_PROMPT_TOKENS - estimatePromptTokens(preparation.previousSummary ?? "");
  return budget >= MIN_BUDGET_TOKENS ? budget : null;
}

function truncateText(text, keepChars) {
  if (text.length <= keepChars) return { text, cut: 0 };
  const head = Math.ceil(keepChars * 0.6);
  const tail = Math.max(0, keepChars - head);
  const cut = text.length - head - tail;
  const marker = "\n[... " + cut + " characters cut by Sumi to fit the context-compaction request ...]\n";
  return {
    text: text.slice(0, head) + marker + (tail > 0 ? text.slice(text.length - tail) : ""),
    cut,
  };
}

function truncateMessage(message, ratio) {
  if (!message || typeof message !== "object") return { message, cut: 0 };
  let cut = 0;
  const shrink = (text) => {
    if (text.length <= MIN_CUT_CHARS) return text;
    const res = truncateText(text, Math.max(MIN_CUT_CHARS, Math.floor(text.length * ratio)));
    cut += res.cut;
    return res.text;
  };
  if (typeof message.content === "string") {
    return { message: { ...message, content: shrink(message.content) }, cut };
  }
  if (!Array.isArray(message.content)) return { message, cut: 0 };
  const content = message.content.map((block) => {
    if (!block || typeof block !== "object") return block;
    if (block.type === "text" && typeof block.text === "string") {
      return { ...block, text: shrink(block.text) };
    }
    if (block.type === "thinking" && typeof block.thinking === "string") {
      return { ...block, thinking: shrink(block.thinking) };
    }
    if (block.type === "toolCall" && block.arguments && typeof block.arguments === "object" && !Array.isArray(block.arguments)) {
      const args = Object.fromEntries(
        Object.entries(block.arguments).map(([k, v]) => [k, typeof v === "string" ? shrink(v) : v]),
      );
      return { ...block, arguments: args };
    }
    return block;
  });
  return { message: { ...message, content }, cut };
}

function omittedNote(count, tokens, timestamp) {
  return {
    role: "user",
    content: [{
      type: "text",
      text: "[Sumi: " + count + " earlier message" + (count === 1 ? "" : "s") + " (about " + tokens + " tokens) were left out of this summary because they did not fit in one summarization request.]",
    }],
    timestamp: typeof timestamp === "number" ? timestamp : Date.now(),
  };
}

export function fitMessagesToBudget(messages, budget) {
  if (!Array.isArray(messages) || messages.length === 0) {
    return { messages: Array.isArray(messages) ? [...messages] : [], before: 0, after: 0, dropped: 0, droppedTokens: 0, truncated: 0, truncatedChars: 0 };
  }
  const sizes = messages.map((m) => estimateMessageTokens(m));
  const before = sizes.reduce((sum, n) => sum + n, 0);
  if (before <= budget) {
    return { messages: [...messages], before, after: before, dropped: 0, droppedTokens: 0, truncated: 0, truncatedChars: 0 };
  }
  const maxSingle = Math.floor(budget / 2);
  let truncated = 0;
  let truncatedChars = 0;
  const fitted = messages.map((message, index) => {
    if (sizes[index] <= maxSingle) return message;
    const res = truncateMessage(message, (maxSingle / sizes[index]) * 0.9);
    if (res.cut === 0) return message;
    truncated += 1;
    truncatedChars += res.cut;
    sizes[index] = estimateMessageTokens(res.message);
    return res.message;
  });
  let total = sizes.reduce((sum, n) => sum + n, 0);
  let start = 0;
  let droppedTokens = 0;
  while (total + (start > 0 ? OMITTED_NOTE_TOKENS : 0) > budget && start < fitted.length - 1) {
    total -= sizes[start];
    droppedTokens += sizes[start];
    start += 1;
  }
  const kept = fitted.slice(start);
  const result = start > 0
    ? [omittedNote(start, droppedTokens, messages[start]?.timestamp), ...kept]
    : kept;
  return {
    messages: result,
    before,
    after: total + (start > 0 ? OMITTED_NOTE_TOKENS : 0),
    dropped: start,
    droppedTokens,
    truncated,
    truncatedChars,
  };
}

export function fitPiCompactionPreparation(preparation, model, trigger) {
  const budget = piCompactionFitBudget(preparation, model, trigger);
  if (budget === null) return null;
  const history = fitMessagesToBudget(preparation.messagesToSummarize, budget);
  const prefix = fitMessagesToBudget(preparation.turnPrefixMessages, budget);
  if (history.dropped + history.truncated + prefix.dropped + prefix.truncated === 0) return null;
  preparation.messagesToSummarize = history.messages;
  preparation.turnPrefixMessages = prefix.messages;
  return {
    trigger,
    budgetTokens: budget,
    tokensBefore: history.before + prefix.before,
    tokensAfter: history.after + prefix.after,
    droppedMessages: history.dropped + prefix.dropped,
    droppedTokens: history.droppedTokens + prefix.droppedTokens,
    truncatedMessages: history.truncated + prefix.truncated,
    truncatedChars: history.truncatedChars + prefix.truncatedChars,
  };
}

export default function sumiCompactionFitExtension(pi) {
  if (!pi || typeof pi.on !== "function") return;
  pi.on("session_before_compact", (event, ctx) => {
    if (!event?.preparation) return undefined;
    fitPiCompactionPreparation(event.preparation, ctx?.model, event.reason);
    return undefined;
  });
}
`

func writeCompactionFitExtension(path string) error {
	want := []byte(compactionFitExtensionSource)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, want) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, want, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
