import React, { useState } from "react";
import {
  ChevronDown,
  ChevronRight,
  Trash2,
  Plus,
  Search,
  Gauge,
  Square,
  Server,
  Lock,
  Link2,
  ClipboardPaste,
  X,
} from "lucide-react";
import { rpc } from "../../api/bridge";
import type { AppSnapshot, Line, LineType } from "../../types";
import { ChainEditor } from "../../components/ChainEditor";
import { LatencyBadge } from "../../components/LatencyBadge";
import { lineTypeBadge } from "../../utils/format";

const ANYTLS_FINGERPRINTS = [
  "chrome",
  "chrome_psk",
  "chrome_padding_psk_shuffle",
  "firefox",
  "edge",
  "safari",
  "ios",
  "android",
  "randomized",
];

const SS_METHODS = [
  "2022-blake3-aes-128-gcm",
  "2022-blake3-aes-256-gcm",
  "2022-blake3-chacha20-poly1305",
  "aes-128-gcm",
  "aes-256-gcm",
  "chacha20-ietf-poly1305",
];

export const LinesTab: React.FC<{
  snap: AppSnapshot;
  onOpenCloudHost: () => void;
  onError: (msg: string | null) => void;
}> = ({ snap, onOpenCloudHost, onError }) => {
  const lang = snap.preferences.language || "zh-CN";
  const en = lang === "en";

  const [query, setQuery] = useState("");
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [showsAddMenu, setShowsAddMenu] = useState(false);
  const [showsLinkModal, setShowsLinkModal] = useState(false);
  const [linkInput, setLinkInput] = useState("");
  const [linkBusy, setLinkBusy] = useState(false);
  const [linkErr, setLinkErr] = useState<string | null>(null);
  const [draggingId, setDraggingId] = useState<string | null>(null);

  const isConnected = snap.phase === "connected";
  const lines = snap.editor.lines.filter(
    (l) => !query.trim() || l.name.toLowerCase().includes(query.trim().toLowerCase())
  );

  const anyTesting = Object.values(snap.latencies).some((r) => r.testing);

  const exec = async (fn: () => Promise<unknown>) => {
    onError(null);
    try {
      await fn();
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e));
    }
  };

  const addNewLine = (type: LineType) => {
    setShowsAddMenu(false);
    const base: Line = {
      id: "",
      name: `${lineTypeBadge(type, lang)} ${snap.editor.lines.length}`,
      type,
      enabled: true,
    };
    if (type === "shadowsocks") {
      base.ss_port = 8388;
      base.ss_method = "2022-blake3-aes-128-gcm";
    } else if (type === "trojan") {
      base.trojan_port = 443;
    } else if (type === "vmess") {
      base.vmess_port = 443;
    } else if (type === "vless") {
      base.vless_port = 443;
      base.vless_flow = "xtls-rprx-vision";
      base.vless_client_fingerprint = "chrome";
      base.udp = true;
    } else if (type === "anytls") {
      base.anytls_port = 443;
      base.anytls_client_fingerprint = "chrome";
    }
    void exec(() => rpc("saveLine", base));
  };

  const handleDropReorder = (targetId: string) => {
    if (!draggingId || draggingId === targetId || targetId === "direct" || query.trim()) return;
    const ids = snap.editor.lines.filter((l) => l.id !== "direct").map((l) => l.id);
    const fromIdx = ids.indexOf(draggingId);
    const toIdx = ids.indexOf(targetId);
    if (fromIdx < 0 || toIdx < 0) return;
    ids.splice(fromIdx, 1);
    ids.splice(toIdx, 0, draggingId);
    setDraggingId(null);
    void exec(() => rpc("reorderLines", { ids }));
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      {/* Top Toolbar */}
      <div
        style={{
          padding: "8px 16px",
          display: "flex",
          alignItems: "center",
          gap: 10,
        }}
      >
        <div style={{ position: "relative", flex: 1, maxWidth: 240 }}>
          <Search
            size={13}
            color="var(--text-secondary)"
            style={{ position: "absolute", left: 9, top: 8 }}
          />
          <input
            className="xd-input"
            style={{ width: "100%", paddingLeft: 28 }}
            placeholder={en ? "Filter lines…" : "搜索线路…"}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>

        <button
          className="xd-capsule-btn"
          onClick={() => {
            if (anyTesting) {
              void rpc("stopLatencyTests");
            } else {
              void exec(() =>
                rpc("testBatchLatency", {
                  ids: lines.filter((l) => l.enabled).map((l) => l.id),
                })
              );
            }
          }}
        >
          {anyTesting ? <Square size={11} /> : <Gauge size={13} />}
          <span>
            {anyTesting
              ? en
                ? "Stop Test"
                : "停止测速"
              : en
                ? "Test Listed"
                : "测当前列表"}
          </span>
        </button>

        <div style={{ flex: 1 }} />
        <span style={{ fontSize: 11.5, color: "var(--text-secondary)" }}>
          {lines.length} {en ? "lines" : "条线路"}
        </span>
      </div>

      {/* Scrollable Card List */}
      <div
        style={{
          flex: 1,
          overflowY: "auto",
          padding: "6px 16px 14px",
          display: "flex",
          flexDirection: "column",
          gap: 8,
        }}
      >
        {lines.map((line) => {
          const isDirect = line.id === "direct";
          const isOpen = Boolean(expanded[line.id]) && !isDirect;

          return (
            <div
              key={line.id}
              draggable={!isDirect && !query.trim() && !isOpen}
              onDragStart={() => setDraggingId(line.id)}
              onDragEnd={() => setDraggingId(null)}
              onDragOver={(e) => e.preventDefault()}
              onDrop={() => handleDropReorder(line.id)}
              className={`xd-card ${!line.enabled ? "disabled-card" : ""}`}
              style={{
                padding: "9px 11px",
                opacity: draggingId === line.id ? 0.25 : undefined,
              }}
            >
              {/* Header Row */}
              <div
                onClick={() => {
                  if (!isDirect) {
                    setExpanded((p) => ({ ...p, [line.id]: !p[line.id] }));
                  }
                }}
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 8,
                  cursor: isDirect ? "default" : "pointer",
                }}
              >
                <span style={{ width: 13, display: "inline-flex", color: "var(--text-secondary)" }}>
                  {isDirect ? (
                    <Lock size={11} />
                  ) : isOpen ? (
                    <ChevronDown size={13} />
                  ) : (
                    <ChevronRight size={13} />
                  )}
                </span>

                <span style={{ fontSize: 13, fontWeight: 600 }}>{line.name}</span>

                <span
                  style={{
                    fontSize: 10.5,
                    padding: "1px 6px",
                    borderRadius: 999,
                    background: "rgba(var(--text-primary-rgb), 0.06)",
                    color: "var(--text-secondary)",
                  }}
                >
                  {lineTypeBadge(line.type, lang)}
                </span>

                <span
                  style={{
                    fontSize: 11.5,
                    color: "var(--text-secondary)",
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap",
                    flex: 1,
                  }}
                >
                  {summarizeLineEndpoint(line)}
                </span>

                <LatencyBadge
                  lineId={line.id}
                  lineType={line.type}
                  record={snap.latencies[line.id]}
                  isConnected={isConnected}
                  lang={lang}
                  onTest={(id) => void exec(() => rpc("testBatchLatency", { ids: [id] }))}
                />

                {!isDirect && (
                  <>
                    <button
                      type="button"
                      className="xd-switch compact"
                      data-checked={String(line.enabled)}
                      onClick={(e) => {
                        e.stopPropagation();
                        void exec(() => rpc("saveLine", { ...line, enabled: !line.enabled }));
                      }}
                    >
                      <span className="xd-switch-thumb" />
                    </button>

                    <button
                      title={en ? "Delete line" : "删除线路"}
                      onClick={(e) => {
                        e.stopPropagation();
                        void exec(() => rpc("deleteLine", { id: line.id }));
                      }}
                      style={{
                        border: "none",
                        background: "transparent",
                        color: "var(--text-secondary)",
                        cursor: "pointer",
                        padding: 3,
                        display: "inline-flex",
                      }}
                    >
                      <Trash2 size={13} />
                    </button>
                  </>
                )}
              </div>

              {isOpen && (
                <LineEditorForm
                  line={line}
                  lang={lang}
                  onSave={(updated) => void exec(() => rpc("saveLine", updated))}
                />
              )}
            </div>
          );
        })}
      </div>

      {/* Bottom AddBar */}
      <div
        style={{
          height: 44,
          padding: "0 16px",
          borderTop: "0.75px solid rgba(var(--divider-rgb), 0.35)",
          background: "rgba(var(--text-primary-rgb), 0.015)",
          display: "flex",
          alignItems: "center",
          justifyContent: "flex-end",
          position: "relative",
        }}
      >
        <button
          className="xd-capsule-btn"
          onClick={() => setShowsAddMenu((v) => !v)}
        >
          <Plus size={13} />
          <span>{en ? "Add Line" : "添加线路"}</span>
        </button>

        {showsAddMenu && (
          <div
            style={{
              position: "absolute",
              bottom: 48,
              right: 16,
              zIndex: 60,
              minWidth: 185,
              padding: 5,
              borderRadius: 10,
              background: "var(--elevated)",
              border: "0.75px solid rgba(var(--divider-rgb), 0.65)",
              boxShadow: "var(--popover-shadow)",
              display: "flex",
              flexDirection: "column",
              gap: 2,
            }}
          >
            <button
              onClick={() => {
                setShowsAddMenu(false);
                setLinkErr(null);
                setLinkInput("");
                setShowsLinkModal(true);
              }}
              style={{
                border: "none",
                background: "transparent",
                padding: "6px 10px",
                borderRadius: 6,
                textAlign: "left",
                fontSize: 12,
                color: "var(--accent)",
                fontWeight: 600,
                display: "flex",
                alignItems: "center",
                gap: 6,
                cursor: "pointer",
              }}
            >
              <Link2 size={13} />
              <span>{en ? "Import Share Link (vless://)…" : "从分享链接导入…"}</span>
            </button>
            <div className="xd-divider" style={{ margin: "3px 0" }} />
            {(
              [
                ["vpn", "Cisco AnyConnect"],
                ["vless", "VLESS + Reality"],
                ["anytls", "AnyTLS"],
                ["trojan", "Trojan"],
                ["shadowsocks", "Shadowsocks"],
                ["vmess", "VMess"],
                ["tailscale", "Tailscale"],
              ] as [LineType, string][]
            ).map(([t, label]) => (
              <button
                key={t}
                onClick={() => addNewLine(t)}
                style={{
                  border: "none",
                  background: "transparent",
                  padding: "6px 10px",
                  borderRadius: 6,
                  textAlign: "left",
                  fontSize: 12,
                  color: "var(--text-primary)",
                  cursor: "pointer",
                }}
              >
                {label}
              </button>
            ))}
            <div className="xd-divider" style={{ margin: "3px 0" }} />
            <button
              onClick={() => {
                setShowsAddMenu(false);
                onOpenCloudHost();
              }}
              style={{
                border: "none",
                background: "transparent",
                padding: "6px 10px",
                borderRadius: 6,
                textAlign: "left",
                fontSize: 12,
                color: "var(--accent)",
                fontWeight: 600,
                display: "flex",
                alignItems: "center",
                gap: 6,
                cursor: "pointer",
              }}
            >
              <Server size={13} />
              <span>{en ? "My Cloud Server…" : "我的云主机…"}</span>
            </button>
          </div>
        )}
      </div>

      {showsLinkModal && (
        <div
          onClick={() => setShowsLinkModal(false)}
          style={{
            position: "fixed",
            inset: 0,
            zIndex: 110,
            background: "rgba(0, 0, 0, 0.36)",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            padding: 20,
          }}
        >
          <div
            onClick={(e) => e.stopPropagation()}
            style={{
              width: 450,
              borderRadius: 14,
              background: "var(--elevated)",
              border: "0.75px solid rgba(var(--divider-rgb), 0.65)",
              boxShadow: "var(--popover-shadow)",
              padding: 18,
              display: "flex",
              flexDirection: "column",
              gap: 11,
            }}
          >
            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
              <div style={{ display: "flex", alignItems: "center", gap: 7 }}>
                <Link2 size={15} color="var(--accent)" />
                <span style={{ fontSize: 14.5, fontWeight: 700 }}>
                  {en ? "Import Line from Share Link" : "从分享链接导入线路"}
                </span>
              </div>
              <button
                onClick={() => setShowsLinkModal(false)}
                style={{
                  border: "none",
                  background: "transparent",
                  color: "var(--text-secondary)",
                  cursor: "pointer",
                }}
              >
                <X size={15} />
              </button>
            </div>

            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
              <span style={{ fontSize: 11.5, color: "var(--text-secondary)" }}>
                {en
                  ? "Supports vless:// (Reality / TLS), ss://, trojan://, vmess://, anytls:// (one per line or Base64)"
                  : "支持 vless://（含 Reality）、ss://、trojan://、vmess://、anytls://（每行一条或 Base64）"}
              </span>
              <button
                className="xd-capsule-btn"
                style={{ height: 24 }}
                onClick={async () => {
                  try {
                    const clip = await navigator.clipboard.readText();
                    if (clip) setLinkInput(clip);
                  } catch {
                    // clipboard denied or empty
                  }
                }}
              >
                <ClipboardPaste size={12} />
                <span>{en ? "Paste" : "粘贴剪贴板"}</span>
              </button>
            </div>

            <textarea
              className="xd-textarea tabular-mono"
              rows={5}
              autoFocus
              placeholder="vless://uuid@host:port?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.microsoft.com&fp=chrome&pbk=...&sid=...#Name"
              value={linkInput}
              onChange={(e) => setLinkInput(e.target.value)}
            />

            {linkErr && (
              <div className="selectable" style={{ fontSize: 11.5, color: "var(--danger)" }}>
                {linkErr}
              </div>
            )}

            <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
              <button className="xd-capsule-btn" onClick={() => setShowsLinkModal(false)}>
                {en ? "Cancel" : "取消"}
              </button>
              <button
                className="xd-capsule-btn primary"
                disabled={linkBusy || !linkInput.trim()}
                onClick={async () => {
                  setLinkBusy(true);
                  setLinkErr(null);
                  try {
                    const created = await rpc<Line[]>("importLineURIs", { content: linkInput });
                    if (created && created.length === 1) {
                      setExpanded((prev) => ({ ...prev, [created[0].id]: true }));
                    }
                    setShowsLinkModal(false);
                  } catch (e) {
                    setLinkErr(e instanceof Error ? e.message : String(e));
                  } finally {
                    setLinkBusy(false);
                  }
                }}
              >
                {linkBusy ? (en ? "Importing…" : "正在导入…") : en ? "Import Line" : "导入线路"}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

function summarizeLineEndpoint(l: Line): string {
  switch (l.type) {
    case "vpn":
      return l.vpn_server || "";
    case "trojan":
      return l.trojan_server ? `${l.trojan_server}:${l.trojan_port || 443}` : "";
    case "shadowsocks":
      return l.ss_server ? `${l.ss_server}:${l.ss_port || ""}` : "";
    case "vmess":
      return l.vmess_server ? `${l.vmess_server}:${l.vmess_port || 443}` : "";
    case "vless":
      return l.vless_server ? `${l.vless_server}:${l.vless_port || 443}` : "";
    case "anytls":
      return l.anytls_server ? `${l.anytls_server}:${l.anytls_port || 443}` : "";
    case "tailscale":
      return l.tailscale_exit_node || "Tailnet";
    default:
      return "";
  }
}

const LineEditorForm: React.FC<{
  line: Line;
  lang: "zh-CN" | "en";
  onSave: (l: Line) => void;
}> = ({ line, lang, onSave }) => {
  const en = lang === "en";
  const [draft, setDraft] = useState<Line>(line);

  const commit = (patch?: Partial<Line>) => {
    const next = { ...draft, ...patch };
    setDraft(next);
    onSave(next);
  };

  return (
    <div
      onClick={(e) => e.stopPropagation()}
      style={{
        padding: "10px 4px 4px 21px",
        display: "flex",
        flexDirection: "column",
        gap: 8,
      }}
    >
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
        <Field label={en ? "Name" : "线路名称"}>
          <input
            className="xd-input"
            value={draft.name}
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
            onBlur={() => commit()}
          />
        </Field>

        {draft.type === "vpn" && (
          <Field label={en ? "Gateway URL / Host" : "AnyConnect 网关地址"}>
            <input
              className="xd-input tabular-mono"
              placeholder="vpn.corp.example:8443"
              value={draft.vpn_server || ""}
              onChange={(e) => setDraft({ ...draft, vpn_server: e.target.value })}
              onBlur={() => commit()}
            />
          </Field>
        )}

        {draft.type === "tailscale" && (
          <Field label={en ? "Exit Node IP / Hostname" : "Exit Node 节点名或 IP"}>
            <input
              className="xd-input tabular-mono"
              placeholder="100.x.y.z"
              value={draft.tailscale_exit_node || ""}
              onChange={(e) => setDraft({ ...draft, tailscale_exit_node: e.target.value })}
              onBlur={() => commit()}
            />
          </Field>
        )}
      </div>

      {draft.type === "vpn" && (
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
          <Field label={en ? "Username" : "账号用户名"}>
            <input
              className="xd-input"
              value={draft.vpn_username || ""}
              onChange={(e) => setDraft({ ...draft, vpn_username: e.target.value })}
              onBlur={() => commit()}
            />
          </Field>
          <Field label={en ? "Password" : "密码"}>
            <input
              className="xd-input"
              type="password"
              value={draft.vpn_password || ""}
              onChange={(e) => setDraft({ ...draft, vpn_password: e.target.value })}
              onBlur={() => commit()}
            />
          </Field>
        </div>
      )}

      {draft.type === "shadowsocks" && (
        <>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 100px", gap: 8 }}>
            <Field label={en ? "Server" : "服务器地址"}>
              <input
                className="xd-input tabular-mono"
                value={draft.ss_server || ""}
                onChange={(e) => setDraft({ ...draft, ss_server: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
            <Field label={en ? "Port" : "端口"}>
              <input
                className="xd-input tabular-mono"
                type="number"
                value={draft.ss_port || ""}
                onChange={(e) => setDraft({ ...draft, ss_port: Number(e.target.value) })}
                onBlur={() => commit()}
              />
            </Field>
          </div>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
            <Field label={en ? "Cipher" : "加密方式"}>
              <select
                className="xd-select"
                value={draft.ss_method || "2022-blake3-aes-128-gcm"}
                onChange={(e) => commit({ ss_method: e.target.value })}
              >
                {SS_METHODS.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
            </Field>
            <Field label={en ? "Password / Key" : "密码 / 密钥"}>
              <input
                className="xd-input tabular-mono"
                type="password"
                value={draft.ss_password || ""}
                onChange={(e) => setDraft({ ...draft, ss_password: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
          </div>
        </>
      )}

      {draft.type === "trojan" && (
        <>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 100px", gap: 8 }}>
            <Field label={en ? "Server" : "服务器地址"}>
              <input
                className="xd-input tabular-mono"
                value={draft.trojan_server || ""}
                onChange={(e) => setDraft({ ...draft, trojan_server: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
            <Field label={en ? "Port" : "端口"}>
              <input
                className="xd-input tabular-mono"
                type="number"
                value={draft.trojan_port || 443}
                onChange={(e) => setDraft({ ...draft, trojan_port: Number(e.target.value) })}
                onBlur={() => commit()}
              />
            </Field>
          </div>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
            <Field label={en ? "Password" : "密码"}>
              <input
                className="xd-input"
                type="password"
                value={draft.trojan_password || ""}
                onChange={(e) => setDraft({ ...draft, trojan_password: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
            <Field label="TLS SNI">
              <input
                className="xd-input tabular-mono"
                value={draft.trojan_sni || ""}
                onChange={(e) => setDraft({ ...draft, trojan_sni: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
          </div>
        </>
      )}

      {draft.type === "anytls" && (
        <>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 100px", gap: 8 }}>
            <Field label={en ? "Server" : "服务器地址"}>
              <input
                className="xd-input tabular-mono"
                value={draft.anytls_server || ""}
                onChange={(e) => setDraft({ ...draft, anytls_server: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
            <Field label={en ? "Port" : "端口"}>
              <input
                className="xd-input tabular-mono"
                type="number"
                value={draft.anytls_port || 443}
                onChange={(e) => setDraft({ ...draft, anytls_port: Number(e.target.value) })}
                onBlur={() => commit()}
              />
            </Field>
          </div>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: 8 }}>
            <Field label={en ? "Password" : "密码"}>
              <input
                className="xd-input"
                type="password"
                value={draft.anytls_password || ""}
                onChange={(e) => setDraft({ ...draft, anytls_password: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
            <Field label="SNI">
              <input
                className="xd-input tabular-mono"
                value={draft.anytls_sni || ""}
                onChange={(e) => setDraft({ ...draft, anytls_sni: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
            <Field label={en ? "uTLS Fingerprint" : "uTLS 浏览器指纹"}>
              <select
                className="xd-select"
                value={draft.anytls_client_fingerprint || "chrome"}
                onChange={(e) => commit({ anytls_client_fingerprint: e.target.value })}
              >
                {ANYTLS_FINGERPRINTS.map((fp) => (
                  <option key={fp} value={fp}>
                    {fp}
                  </option>
                ))}
              </select>
            </Field>
          </div>
        </>
      )}

      {draft.type === "vless" && (
        <>
          <div style={{ display: "flex", justifyContent: "flex-end" }}>
            <button
              className="xd-capsule-btn"
              style={{ height: 23, fontSize: 11 }}
              onClick={async () => {
                try {
                  const clip = await navigator.clipboard.readText();
                  if (!clip.trim()) return;
                  const parsed = await rpc<Line[]>("parseLineURIs", { content: clip });
                  if (parsed && parsed.length > 0 && parsed[0].type === "vless") {
                    const p = parsed[0];
                    commit({
                      name: p.name || draft.name,
                      vless_server: p.vless_server,
                      vless_port: p.vless_port,
                      vless_uuid: p.vless_uuid,
                      vless_flow: p.vless_flow,
                      vless_sni: p.vless_sni,
                      vless_client_fingerprint: p.vless_client_fingerprint,
                      vless_alpn: p.vless_alpn,
                      vless_reality_public_key: p.vless_reality_public_key,
                      vless_reality_short_id: p.vless_reality_short_id,
                      vless_packet_encoding: p.vless_packet_encoding,
                      udp: p.udp,
                      tfo: p.tfo,
                      allow_insecure: p.allow_insecure,
                    });
                  }
                } catch {
                  // ignore invalid clipboard
                }
              }}
            >
              <ClipboardPaste size={11.5} />
              <span>{en ? "Fill from Clipboard vless://" : "从剪贴板 vless:// 链接填充"}</span>
            </button>
          </div>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 100px 1fr", gap: 8 }}>
            <Field label={en ? "Server" : "服务器地址"}>
              <input
                className="xd-input tabular-mono"
                value={draft.vless_server || ""}
                onChange={(e) => setDraft({ ...draft, vless_server: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
            <Field label={en ? "Port" : "端口"}>
              <input
                className="xd-input tabular-mono"
                type="number"
                value={draft.vless_port || 443}
                onChange={(e) => setDraft({ ...draft, vless_port: Number(e.target.value) })}
                onBlur={() => commit()}
              />
            </Field>
            <Field label="UUID">
              <input
                className="xd-input tabular-mono"
                type="password"
                placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
                value={draft.vless_uuid || ""}
                onChange={(e) => setDraft({ ...draft, vless_uuid: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
          </div>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: 8 }}>
            <Field label={en ? "SNI / Camouflage Domain" : "SNI（Reality 伪装域名）"}>
              <input
                className="xd-input tabular-mono"
                placeholder="www.microsoft.com"
                value={draft.vless_sni || ""}
                onChange={(e) => setDraft({ ...draft, vless_sni: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
            <Field label="Flow">
              <select
                className="xd-select"
                value={draft.vless_flow || ""}
                onChange={(e) => commit({ vless_flow: e.target.value })}
              >
                <option value="xtls-rprx-vision">xtls-rprx-vision</option>
                <option value="">{en ? "None (Standard)" : "无（标准流）"}</option>
              </select>
            </Field>
            <Field label={en ? "uTLS Fingerprint" : "uTLS 浏览器指纹"}>
              <select
                className="xd-select"
                value={draft.vless_client_fingerprint || "chrome"}
                onChange={(e) => commit({ vless_client_fingerprint: e.target.value })}
              >
                {ANYTLS_FINGERPRINTS.map((fp) => (
                  <option key={fp} value={fp}>
                    {fp}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 160px", gap: 8 }}>
            <Field label={en ? "Reality Public Key (optional for plain TLS)" : "Reality 公钥（Public Key，普通 TLS 可留空）"}>
              <input
                className="xd-input tabular-mono"
                placeholder={en ? "Base64URL X25519 public key" : "43 位 X25519 公钥"}
                value={draft.vless_reality_public_key || ""}
                onChange={(e) => setDraft({ ...draft, vless_reality_public_key: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
            <Field label={en ? "Reality Short ID" : "Reality Short ID"}>
              <input
                className="xd-input tabular-mono"
                placeholder={en ? "0–16 hex chars" : "0–16 位十六进制"}
                value={draft.vless_reality_short_id || ""}
                onChange={(e) => setDraft({ ...draft, vless_reality_short_id: e.target.value })}
                onBlur={() => commit()}
              />
            </Field>
          </div>
        </>
      )}

      {draft.type === "vmess" && (
        <div style={{ display: "grid", gridTemplateColumns: "1fr 90px 1fr", gap: 8 }}>
          <Field label={en ? "Server" : "服务器地址"}>
            <input
              className="xd-input tabular-mono"
              value={draft.vmess_server || ""}
              onChange={(e) => setDraft({ ...draft, vmess_server: e.target.value })}
              onBlur={() => commit()}
            />
          </Field>
          <Field label={en ? "Port" : "端口"}>
            <input
              className="xd-input tabular-mono"
              type="number"
              value={draft.vmess_port || 443}
              onChange={(e) => setDraft({ ...draft, vmess_port: Number(e.target.value) })}
              onBlur={() => commit()}
            />
          </Field>
          <Field label="UUID">
            <input
              className="xd-input tabular-mono"
              value={draft.vmess_uuid || ""}
              onChange={(e) => setDraft({ ...draft, vmess_uuid: e.target.value })}
              onBlur={() => commit()}
            />
          </Field>
        </div>
      )}

      {draft.type === "tailscale" && (
        <label style={{ display: "inline-flex", alignItems: "center", gap: 8, fontSize: 12 }}>
          <button
            type="button"
            className="xd-switch compact"
            data-checked={String(Boolean(draft.tailscale_magic_dns))}
            onClick={() => commit({ tailscale_magic_dns: !draft.tailscale_magic_dns })}
          >
            <span className="xd-switch-thumb" />
          </button>
          <span>{en ? "Enable Tailnet MagicDNS" : "启用 Tailnet MagicDNS 解析"}</span>
        </label>
      )}
    </div>
  );
};

// --- Line Groups Tab (DESIGN.md §7: Unified Automatic URLTest / Fixed Selector list) ---

export const LineGroupsTab: React.FC<{
  snap: AppSnapshot;
  onError: (msg: string | null) => void;
}> = ({ snap, onError }) => {
  const lang = snap.preferences.language || "zh-CN";
  const en = lang === "en";

  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [addingMemberFor, setAddingMemberFor] = useState<string | null>(null);

  const [editingChain, setEditingChain] = useState<Line | null>(null);
  const groups = snap.editor.line_groups;
  const candidatePool = snap.editor.all_lines.filter(
    (l) => l.type !== "chain" && l.id !== "direct" && l.type !== "vpn" && l.type !== "tailscale" && l.enabled
  );

  const exec = async (fn: () => Promise<unknown>) => {
    onError(null);
    try {
      await fn();
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div
        style={{
          flex: 1,
          overflowY: "auto",
          padding: "10px 16px 14px",
          display: "flex",
          flexDirection: "column",
          gap: 8,
        }}
      >
        {editingChain && (
          <ChainEditor key={editingChain.id} chain={editingChain} lines={snap.editor.all_lines} en={en}
            onCancel={() => setEditingChain(null)} onSave={(updated) => exec(async () => {
              await rpc("saveLineGroup", updated);
              setEditingChain(null);
            })} />
        )}
        {groups.length === 0 && !editingChain && (
          <div style={{ padding: 28, textAlign: "center", color: "var(--text-secondary)" }}>
            {en
              ? "No line groups yet. Create a group to auto-select fastest nodes or switch manually."
              : "暂无线路组。点击右下角「添加线路组」可组合多条节点自动测速择优或手动选线。"}
          </div>
        )}

        {groups.map((grp) => {
          if (grp.type === "chain") {
            if (editingChain?.id === grp.id) return null;
            const path = (grp.group_members || []).map((id) => snap.editor.all_lines.find(l => l.id === id)?.name || id).join(" → ");
            return <div key={grp.id} className="xd-card" style={{ padding: 10 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <Link2 size={14} /><strong style={{ flex: 1 }}>{grp.name}</strong>
                <LatencyBadge lineId={grp.id} lineType="chain" lang={lang} record={snap.latencies[grp.id]}
                  isConnected={snap.phase === "connected" && snap.active_lines?.some(l => l.id === grp.id)}
                  onTest={(id) => void exec(() => rpc("testBatchLatency", { ids: [id] }))} />
                <button className="xd-capsule-btn" onClick={() => setEditingChain(grp)}>{en ? "Edit chain" : "编辑链路"}</button>
                <button className="xd-capsule-btn" aria-label={en ? "Delete chain" : "删除链路"}
                  onClick={() => void exec(() => rpc("deleteLineGroup", { id: grp.id }))}><Trash2 size={13} /></button>
              </div>
              <small>{path} ({en ? "exit" : "出口"})</small>
            </div>;
          }
          const isOpen = Boolean(expanded[grp.id]);
          const members = grp.group_members || [];
          const isAuto = grp.type === "urltest";

          return (
            <div key={grp.id} className="xd-card" style={{ padding: "9px 11px" }}>
              <div
                onClick={() => setExpanded((p) => ({ ...p, [grp.id]: !p[grp.id] }))}
                style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer" }}
              >
                {isOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                <span style={{ fontSize: 13, fontWeight: 600 }}>{grp.name}</span>
                <span
                  style={{
                    fontSize: 10.5,
                    padding: "1px 6px",
                    borderRadius: 999,
                    background: "rgba(var(--accent-rgb), 0.1)",
                    color: "var(--accent)",
                  }}
                >
                  {isAuto ? (en ? "Auto (URLTest)" : "自动选择") : en ? "Manual" : "固定选择"}
                </span>
                <span style={{ fontSize: 11.5, color: "var(--text-secondary)", flex: 1 }}>
                  {members.length} {en ? "members" : "个成员"}
                </span>
                <button
                  className="xd-capsule-btn"
                  style={{ height: 23, padding: "0 8px" }}
                  onClick={(e) => {
                    e.stopPropagation();
                    void exec(() => rpc("testBatchLatency", { ids: [grp.id] }));
                  }}
                >
                  <Gauge size={11} />
                  <span>{en ? "Test Group" : "整组测速"}</span>
                </button>
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    void exec(() => rpc("deleteLineGroup", { id: grp.id }));
                  }}
                  style={{
                    border: "none",
                    background: "transparent",
                    color: "var(--text-secondary)",
                    cursor: "pointer",
                    padding: 3,
                  }}
                >
                  <Trash2 size={13} />
                </button>
              </div>

              {isOpen && (
                <div
                  onClick={(e) => e.stopPropagation()}
                  style={{
                    padding: "10px 4px 4px 21px",
                    display: "flex",
                    flexDirection: "column",
                    gap: 8,
                  }}
                >
                  <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
                    <input
                      className="xd-input"
                      style={{ flex: 1 }}
                      defaultValue={grp.name}
                      onBlur={(e) => {
                        if (e.target.value.trim() && e.target.value !== grp.name) {
                          void exec(() =>
                            rpc("saveLineGroup", { ...grp, name: e.target.value.trim() })
                          );
                        }
                      }}
                    />
                    <button
                      className="xd-capsule-btn"
                      onClick={() =>
                        setAddingMemberFor(addingMemberFor === grp.id ? null : grp.id)
                      }
                    >
                      <Plus size={12} />
                      <span>{en ? "Add Member" : "添加线路或组"}</span>
                    </button>
                  </div>

                  {/* Unified Member Table: Row 0 = Automatic URLTest, followed by up to 7 visible member rows */}
                  <div
                    style={{
                      maxHeight: 8 * 32,
                      overflowY: "auto",
                      border: "0.75px solid rgba(var(--divider-rgb), 0.45)",
                      borderRadius: 8,
                      background: "var(--canvas)",
                    }}
                  >
                    <div
                      onClick={() =>
                        void exec(() =>
                          rpc("saveLineGroup", {
                            ...grp,
                            type: "urltest",
                            group_default: "",
                          })
                        )
                      }
                      style={{
                        height: 32,
                        padding: "0 10px",
                        display: "flex",
                        alignItems: "center",
                        gap: 8,
                        borderBottom: "0.75px solid rgba(var(--divider-rgb), 0.3)",
                        background: isAuto ? "rgba(var(--selection-rgb), 0.12)" : "transparent",
                        cursor: "pointer",
                        fontWeight: isAuto ? 600 : 400,
                      }}
                    >
                      <span>{isAuto ? "●" : "○"}</span>
                      <span style={{ flex: 1 }}>{en ? "Automatic Selection" : "自动选择（按延迟择优）"}</span>
                    </div>

                    {members.map((mid) => {
                      const mLine = snap.editor.all_lines.find((l) => l.id === mid);
                      const selected = !isAuto && grp.group_default === mid;
                      return (
                        <div
                          key={mid}
                          onClick={() =>
                            void exec(() =>
                              rpc("saveLineGroup", {
                                ...grp,
                                type: "selector",
                                group_default: mid,
                              })
                            )
                          }
                          style={{
                            height: 32,
                            padding: "0 10px",
                            display: "flex",
                            alignItems: "center",
                            gap: 8,
                            borderBottom: "0.75px solid rgba(var(--divider-rgb), 0.22)",
                            background: selected
                              ? "rgba(var(--selection-rgb), 0.12)"
                              : "transparent",
                            cursor: "pointer",
                          }}
                        >
                          <span>{selected ? "●" : "○"}</span>
                          <span style={{ flex: 1, fontWeight: selected ? 600 : 400 }}>
                            {mLine?.name || mid}
                          </span>
                          <LatencyBadge
                            lineId={mid}
                            lineType={mLine?.type}
                            record={snap.latencies[mid]}
                            isConnected={snap.phase === "connected"}
                            lang={lang}
                            onTest={(id) =>
                              void exec(() => rpc("testBatchLatency", { ids: [id] }))
                            }
                          />
                          <button
                            onClick={(e) => {
                              e.stopPropagation();
                              const nextMembers = members.filter((x) => x !== mid);
                              void exec(() =>
                                rpc("saveLineGroup", {
                                  ...grp,
                                  group_members: nextMembers,
                                  group_default:
                                    grp.group_default === mid
                                      ? nextMembers[0] || ""
                                      : grp.group_default,
                                })
                              );
                            }}
                            style={{
                              border: "none",
                              background: "transparent",
                              color: "var(--text-secondary)",
                              cursor: "pointer",
                            }}
                          >
                            ✕
                          </button>
                        </div>
                      );
                    })}
                  </div>

                  {addingMemberFor === grp.id && (
                    <div
                      style={{
                        padding: 8,
                        borderRadius: 8,
                        background: "var(--elevated)",
                        border: "0.75px solid rgba(var(--divider-rgb), 0.55)",
                        display: "flex",
                        flexWrap: "wrap",
                        gap: 6,
                      }}
                    >
                      {candidatePool
                        .filter((c) => c.id !== grp.id && !members.includes(c.id))
                        .map((cand) => (
                          <button
                            key={cand.id}
                            className="xd-capsule-btn"
                            style={{ height: 24 }}
                            onClick={() =>
                              void exec(() =>
                                rpc("saveLineGroup", {
                                  ...grp,
                                  group_members: [...members, cand.id],
                                })
                              )
                            }
                          >
                            <Plus size={11} />
                            <span>{cand.name}</span>
                          </button>
                        ))}
                    </div>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>

      {/* Add Group Bar */}
      <div
        style={{
          height: 44,
          padding: "0 16px",
          borderTop: "0.75px solid rgba(var(--divider-rgb), 0.35)",
          background: "rgba(var(--text-primary-rgb), 0.015)",
          display: "flex",
          alignItems: "center",
          justifyContent: "flex-end",
        }}
      >
        <button className="xd-capsule-btn" style={{ marginRight: 8 }} onClick={() => {
          setEditingChain({ id: "", type: "chain", enabled: true, name: en ? "Home chain" : "在家链路", group_members: ["", ""] });
        }}><Link2 size={13} />{en ? "Add proxy chain" : "添加链式代理"}</button>
        <button
          className="xd-capsule-btn"
          disabled={candidatePool.length === 0}
          onClick={() =>
            void exec(() =>
              rpc("saveLineGroup", {
                id: "",
                name: `${en ? "Group" : "线路组"} ${groups.length + 1}`,
                type: "urltest",
                enabled: true,
                group_members: [candidatePool[0].id],
              })
            )
          }
        >
          <Plus size={13} />
          <span>{en ? "Add Line Group" : "添加线路组"}</span>
        </button>
      </div>
    </div>
  );
};

const Field: React.FC<{ label: string; children: React.ReactNode }> = ({
  label,
  children,
}) => (
  <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
    <label style={{ fontSize: 11, color: "var(--text-secondary)" }}>{label}</label>
    {children}
  </div>
);
