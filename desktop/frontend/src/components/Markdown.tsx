import { createContext, memo, useContext, useEffect, useDeferredValue, useLayoutEffect, useMemo, useRef, useState } from "react";
import ReactMarkdown from "react-markdown";
import type { Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import rehypeKatex from "rehype-katex";
import "katex/dist/katex.min.css";
import { CodeViewer } from "./CodeViewer";
import { MermaidViewer } from "./MermaidViewer";
import { openAttachmentViewer } from "./AttachmentViewer";
import type { ViewerTarget } from "./AttachmentViewer";
import { normalizeMath } from "./mathNormalize";
import { baseName } from "../lib/attachmentDisplay";
import { app, openExternal } from "../lib/bridge";

// Markdown rendering via react-markdown + remark-gfm (tables, task lists,
// strike, autolinks) and remark-math + rehype-katex for $/$$ KaTeX math.
// Fenced code blocks go through CodeViewer for syntax highlighting; inline
// code is a styled <code>. Links open in the system browser.
//
// The math pre-pass in mathNormalize normalises LLM-native \(…\)/\[…\]
// delimiters to the $/$$ syntax remark-math understands, gates single-$
// pairs through a classifier to avoid false positives on $5, $PATH, etc.,
// and runs KaTeX-specific normalisations (text-mode escapes, |→\vert).

const STREAMING_CURSOR_CLASS = "cursor";

// Local attachment images inside one Markdown block are peers of each other:
// clicking any of them opens the lightbox with ‹ › navigation across the whole
// message, so a run of rendered images browses as a single gallery. The set is
// derived from the rendered text and published through context so each MdImage
// can hand the same list to openAttachmentViewer without prop-drilling.
const attachmentRefPattern = /\.fairpeer\/attachments\/[^\s)"'<>]+/g;
const MdImagePeers = createContext<ViewerTarget[]>([]);

// Inject a blinking cursor span at the end of the last inline content node
// inside the container, skipping code blocks entirely.  Called from
// useLayoutEffect so the cursor appears synchronously before paint.
function injectStreamingCursor(container: HTMLElement): void {
  // Remove any cursor injected by a previous render cycle.
  container
    .querySelectorAll(`.${STREAMING_CURSOR_CLASS}`)
    .forEach((el) => el.remove());

  // Walk the rendered tree and collect every text node outside <pre> blocks.
  const walker = document.createTreeWalker(
    container,
    NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT,
    {
      acceptNode(node) {
        if (node.nodeType === Node.ELEMENT_NODE) {
          const tag = (node as Element).tagName;
          // Skip entire code-block subtrees.
          if (tag === "PRE") return NodeFilter.FILTER_REJECT;
          return NodeFilter.FILTER_SKIP;
        }
        // Accept text nodes (but reject whitespace-only noise).
        if (node.nodeType === Node.TEXT_NODE) {
          return (node as Text).data.trim()
            ? NodeFilter.FILTER_ACCEPT
            : NodeFilter.FILTER_SKIP;
        }
        return NodeFilter.FILTER_SKIP;
      },
    },
  );

  let lastText: Text | null = null;
  while (walker.nextNode()) lastText = walker.currentNode as Text;

  const cursor = document.createElement("span");
  cursor.className = STREAMING_CURSOR_CLASS;
  cursor.dataset.streamingCursor = "true";

  if (lastText?.parentElement) {
    lastText.parentElement.appendChild(cursor);
  } else {
    // Fallback: no visible text yet (empty streaming start).
    container.appendChild(cursor);
  }
}

function removeStreamingCursor(container: HTMLElement): void {
  container
    .querySelectorAll(`.${STREAMING_CURSOR_CLASS}`)
    .forEach((el) => el.remove());
}

// MdImage renders markdown images. Relative `.fairpeer/attachments/...` paths
// can't be loaded by a bare <img> (the webview has no disk base URL), so fetch
// the data URL via the kernel — same mechanism UserMessage uses for previews.
// http(s)/data: URLs render natively. Clicking opens the attachment lightbox
// (or the system browser for remote URLs) so inline images are inspectable.
function MdImage({ src, alt }: { src?: string; alt?: string }) {
  const s = String(src ?? "");
  const peers = useContext(MdImagePeers);
  const [dataUrl, setDataUrl] = useState<string>("");
  useEffect(() => {
    if (!s.startsWith(".fairpeer/attachments/")) return;
    let cancelled = false;
    app.AttachmentDataURL(s)
      .then((url) => { if (!cancelled) setDataUrl(url); })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [s]);
  const resolved = dataUrl || (s.startsWith(".fairpeer/attachments/") ? "" : s);
  if (!resolved) return null;
  const name = alt?.trim() || s.split("/").filter(Boolean).pop() || s;
  if (s.startsWith(".fairpeer/attachments/")) {
    return (
      <button
        type="button"
        className="md-img-open"
        title={name}
        onClick={() => openAttachmentViewer({ path: s, name, kind: "image", source: "attachment", previewUrl: dataUrl || undefined, siblings: peers })}
      >
        <img className="md-img" src={resolved} alt={alt ?? ""} loading="lazy" draggable={false} />
      </button>
    );
  }
  return (
    <img
      className="md-img md-img--remote"
      src={resolved}
      alt={alt ?? ""}
      loading="lazy"
      draggable={false}
      onClick={() => openExternal(s)}
    />
  );
}

const components: Components = {
  pre: ({ children }) => <>{children}</>,
  img: ({ src, alt }) => <MdImage src={src} alt={alt} />,
  code: ({ className, children }) => {
    const text = String(children ?? "");
    const match = /language-([\w-]+)/.exec(className ?? "");
    const isBlock = match !== null || text.includes("\n");
    if (isBlock) {
      if (match?.[1] === "mermaid") {
        return <MermaidViewer chart={text.replace(/\n$/, "")} />;
      }
      return <CodeViewer value={text.replace(/\n$/, "")} language={match?.[1]} maxHeight={360} />;
    }
    return <code className="md-code">{children}</code>;
  },
  a: ({ href, children }) => (
    <a
      href={href}
      onClick={(e) => {
        e.preventDefault();
        if (href) openExternal(href);
      }}
      onAuxClick={(e) => {
        e.preventDefault();
        if (href) openExternal(href);
      }}
      onMouseDown={(e) => {
        if (e.button === 1) e.preventDefault();
      }}
    >
      {children}
    </a>
  ),
};

export const Markdown = memo(function Markdown({
  text,
  showCursor,
}: {
  text: string;
  showCursor?: boolean;
}) {
  const deferred = useDeferredValue(text);
  const containerRef = useRef<HTMLDivElement>(null);

  // Peer set for the lightbox gallery — only worth computing when the block
  // actually carries local attachment paths.
  const peers = useMemo<ViewerTarget[]>(() => {
    if (!deferred.includes(".fairpeer/attachments/")) return [];
    const seen = new Set<string>();
    const out: ViewerTarget[] = [];
    for (const match of deferred.matchAll(attachmentRefPattern)) {
      const path = match[0];
      if (seen.has(path)) continue;
      seen.add(path);
      out.push({ path, name: baseName(path), kind: "image", source: "attachment" });
    }
    return out;
  }, [deferred]);

  // Inject / remove cursor after every React render cycle so the cursor
  // always sits at the tail of the current streaming content — without
  // ever touching the raw Markdown string that ReactMarkdown parses.
  useLayoutEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    if (showCursor) {
      injectStreamingCursor(el);
    } else {
      removeStreamingCursor(el);
    }
  });

  return (
    <div className="md" ref={containerRef}>
      <MdImagePeers.Provider value={peers}>
        <ReactMarkdown
          remarkPlugins={[remarkGfm, remarkMath]}
          rehypePlugins={[rehypeKatex]}
          components={components}
        >
          {normalizeMath(deferred)}
        </ReactMarkdown>
      </MdImagePeers.Provider>
    </div>
  );
});
