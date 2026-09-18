import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { app } from "../../lib/bridge";
import { useI18n } from "../../lib/i18n";
import type { NetDevChainKind, NetDevChainNode, NetDevIncidentCase, NetDevInvestigationChain } from "../../lib/types";

// ChainBoard — 调查链屏（DASHBOARD spec §4.6）。六列分层 SVG（选型记录
// §4.10：不引 React Flow，复用 TopologyMap 的确定性分层模式）。布局是
// 纯计算：列 x = 序号×列宽，节点 y 按建造序堆叠——同一份数据永远画同
// 一张图（可截图、可回归对比）。节点只带标题级摘要，点击深链原文
// （结果归位）；节点上限 150 + 同型折叠由后端负责。

const KIND_ORDER: NetDevChainKind[] = ["event", "action", "evidence", "conclusion", "remediation", "verification"];
const KIND_COLOR: Record<string, string> = {
  event: "#f5a524", action: "#46a758", evidence: "#4f8ef7",
  conclusion: "#e5484d", remediation: "#a78bfa", verification: "#63c9b8",
};
const EDGE_LABEL: Record<string, string> = {
  event: "ndv.chain.col.event", action: "ndv.chain.col.action", evidence: "ndv.chain.col.evidence",
  conclusion: "ndv.chain.col.conclusion", remediation: "ndv.chain.col.remediation", verification: "ndv.chain.col.verification",
};

const COL_W = 250, NODE_H = 52, NODE_GAP = 14, PAD = 16, HEAD = 30;

interface Props {
  caseID?: string;
  findingID?: string;
  onJump?: (j: { tab: string; filter?: string }) => void;
  onFocusDevice?: (device: string) => void;
}

function layout(chain: NetDevInvestigationChain) {
  const byCol: Record<string, NetDevChainNode[]> = {};
  for (const n of chain.nodes) (byCol[n.kind] ??= []).push(n);
  const pos: Record<string, { x: number; y: number; w: number; h: number }> = {};
  let maxRows = 1;
  KIND_ORDER.forEach((kind, ci) => {
    const list = byCol[kind] ?? [];
    maxRows = Math.max(maxRows, list.length);
    list.forEach((n, ri) => {
      pos[n.id] = { x: PAD + ci * COL_W, y: HEAD + ri * (NODE_H + NODE_GAP), w: COL_W - NODE_GAP, h: NODE_H };
    });
  });
  return { pos, width: PAD * 2 + KIND_ORDER.length * COL_W, height: HEAD + maxRows * (NODE_H + NODE_GAP) + PAD };
}

export default function ChainBoard({ caseID, findingID, onJump, onFocusDevice }: Props) {
  const { t } = useI18n();
  const [chain, setChain] = useState<NetDevInvestigationChain | null>(null);
  const [cases, setCases] = useState<NetDevIncidentCase[]>([]);
  const [sel, setSel] = useState(caseID ?? "");
  const [hl, setHl] = useState<string | null>(null); // node hover → highlight neighborhood
  const [view, setView] = useState({ k: 1, tx: 0, ty: 0 });
  const dragRef = useRef<{ x: number; y: number; tx: number; ty: number } | null>(null);
  const canvasRef = useRef<HTMLDivElement | null>(null);
  const fittedRef = useRef(false); // 用户一旦手动缩放/拖拽，不再自动适配

  const load = useCallback(() => {
    app.NetDevInvestigationChain(sel, findingID ?? "", 24)
      .then(c => { if (c) setChain(c); })
      .catch(() => {});
  }, [sel, findingID]);
  useEffect(() => { load(); }, [load]);
  useEffect(() => {
    app.NetDevCases().then(cs => setCases(cs ?? [])).catch(() => {});
    const on = (e: Event) => {
      const screens = (e as CustomEvent<{ screens?: string[] }>).detail?.screens ?? [];
      if (screens.includes("chain")) load();
    };
    window.addEventListener("hiq:netdev-dash", on);
    return () => window.removeEventListener("hiq:netdev-dash", on);
  }, [load]);

  const { pos, width, height } = useMemo(() => chain ? layout(chain) : { pos: {}, width: 0, height: 0 }, [chain]);
  // 初始适配（§4.6 六列同屏）：容器宽度装不下画布时按宽比缩放到恰好放下。
  // 用户交互（拖拽/缩放）后 fittedRef 置位，不再覆盖用户的选择。
  useEffect(() => {
    if (!chain || fittedRef.current) return;
    const el = canvasRef.current;
    if (!el) return;
    const fit = () => {
      if (fittedRef.current) return;
      const cw = el.clientWidth - 8;
      if (cw > 0 && width > cw) {
        setView(v => ({ ...v, k: Math.max(0.4, cw / width) }));
      }
    };
    fit();
    const ro = new ResizeObserver(fit);
    ro.observe(el);
    return () => ro.disconnect();
  }, [chain, width]);

  const neighbors = useMemo(() => {
    if (!chain || !hl) return null;
    const set = new Set<string>([hl]);
    for (const e of chain.edges) {
      if (e.from === hl) set.add(e.to);
      if (e.to === hl) set.add(e.from);
    }
    return set;
  }, [chain, hl]);

  if (!chain) return <div className="ndv__card" style={{ padding: 16 }}>{t("ndv.chain.empty")}</div>;

  const jumpNode = (n: NetDevChainNode) => {
    if (n.device) onFocusDevice?.(n.device);
    if (n.ref_type === "finding" && n.ref_id) onJump?.({ tab: "findings", filter: `id:${n.ref_id}` });
    else if (n.ref_type === "proposal" && n.ref_id) onJump?.({ tab: "proposals", filter: `id:${n.ref_id}` });
    else if (n.ref_type === "audit") onJump?.({ tab: "audit" });
  };

  return (
    <div className="ndv-chain">
      <div className="ndv-chain__bar">
        <select className="mem-input" value={sel} onChange={e => setSel(e.target.value)} style={{ maxWidth: 240 }}>
          <option value="">{t("ndv.chain.autoCase")}</option>
          {cases.map(c => <option key={c.id} value={c.id}>{c.id} {c.title}</option>)}
        </select>
        {chain.case_title && <span className="ndv-chain__casetitle">{chain.case_title}</span>}
        <span className="ndv-chain__counts">
          {KIND_ORDER.map(k => `${t(EDGE_LABEL[k] as never)} ${chain.counts[k] ?? 0}`).join(" · ")}
        </span>
        {chain.truncated && <span className="ndv-chain__trunc">{t("ndv.chain.truncated")}</span>}
        <span className="dim" style={{ marginLeft: "auto", fontSize: 11 }}>{t("ndv.chain.hint")}</span>
      </div>

      <div
        ref={canvasRef}
        className="ndv-chain__canvas"
        onWheel={e => {
          if (!e.ctrlKey) return; // 普通滚动交给容器；Ctrl+滚轮缩放
          e.preventDefault();
          fittedRef.current = true;
          setView(v => ({ ...v, k: Math.min(2, Math.max(0.4, v.k * (e.deltaY < 0 ? 1.1 : 0.9))) }));
        }}
        onPointerDown={e => { fittedRef.current = true; dragRef.current = { x: e.clientX, y: e.clientY, tx: view.tx, ty: view.ty }; }}
        onPointerMove={e => {
          const d = dragRef.current;
          if (!d) return;
          setView(v => ({ ...v, tx: d.tx + (e.clientX - d.x), ty: d.ty + (e.clientY - d.y) }));
        }}
        onPointerUp={() => { dragRef.current = null; }}
        onPointerLeave={() => { dragRef.current = null; }}
      >
        <svg width={width} height={height} style={{ transform: `translate(${view.tx}px, ${view.ty}px) scale(${view.k})`, transformOrigin: "0 0" }} role="img" aria-label={t("ndv.chain.aria")}>
          {/* column captions */}
          {KIND_ORDER.map((kind, ci) => (
            <text key={kind} x={PAD + ci * COL_W + 4} y={16} fontSize={11} fill={KIND_COLOR[kind]} fontWeight={700}>{t(EDGE_LABEL[kind] as never)}</text>
          ))}
          {/* edges */}
          {chain.edges.map((e, i) => {
            const a = pos[e.from], b = pos[e.to];
            if (!a || !b) return null;
            const x1 = a.x + a.w, y1 = a.y + a.h / 2, x2 = b.x, y2 = b.y + b.h / 2;
            const dim = neighbors && !(neighbors.has(e.from) && neighbors.has(e.to));
            const mx = (x1 + x2) / 2;
            return (
              <g key={i} opacity={dim ? 0.15 : 0.85}>
                <path d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`} fill="none" stroke="#8b93a1" strokeWidth="1.4" markerEnd="url(#chainArrow)" />
                <text x={mx} y={(y1 + y2) / 2 - 4} fontSize={9.5} fill="#8b93a1" textAnchor="middle">{e.label}</text>
              </g>
            );
          })}
          <defs>
            <marker id="chainArrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
              <path d="M 0 0 L 10 5 L 0 10 z" fill="#8b93a1" />
            </marker>
          </defs>
          {/* nodes */}
          {chain.nodes.map(n => {
            const p = pos[n.id];
            if (!p) return null;
            const dim = neighbors && !neighbors.has(n.id);
            const lines = [n.label, [n.device, n.at, n.status].filter(Boolean).join(" · ")];
            return (
              <g key={n.id} transform={`translate(${p.x}, ${p.y})`} opacity={dim ? 0.25 : 1}
                style={{ cursor: "pointer" }}
                onMouseEnter={() => setHl(n.id)} onMouseLeave={() => setHl(null)}
                onClick={() => jumpNode(n)}>
                <rect width={p.w} height={p.h} rx={6} fill="var(--bg-elevated, #23272e)" stroke="#3a404c" />
                <rect width={3.5} height={p.h} rx={2} fill={KIND_COLOR[n.kind]} />
                <text x={10} y={20} fontSize={11.5} fill="var(--fg, #e6e9ef)" fontWeight={600}>
                  {lines[0].length > 30 ? lines[0].slice(0, 29) + "…" : lines[0]}{(n.group ?? 0) > 1 ? ` ×${n.group}` : ""}
                </text>
                <text x={10} y={37} fontSize={9.5} fill="#9aa3b2">{lines[1].slice(0, 44)}</text>
              </g>
            );
          })}
        </svg>
      </div>

      {(chain.timeline?.length ?? 0) > 0 && (
        <div className="ndv-chain__tl">
          {(chain.timeline ?? []).slice(-12).reverse().map((e, i) => (
            <span key={i} className="ndv-chain__tlev" role="button">
              <span className="dim">{e.time?.slice(5, 21) || ""}</span>
              <span className={`ndv-chain__tlkind ndv-chain__tlkind--${e.kind}`}>{t(`ndv.chain.tl.${e.kind}` as never)}</span>
              <span>{e.device ? `${e.device} · ` : ""}{e.title || e.detail}</span>
            </span>
          ))}
        </div>
      )}
    </div>
  );
}
