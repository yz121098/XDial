import React, { useRef, useState } from "react";
import {
  Check,
  Circle,
  Plus,
  FileText,
  Link2,
  RefreshCw,
  Download,
  Trash2,
  X,
} from "lucide-react";
import { rpc } from "../api/bridge";
import type { ProfileSourceItem } from "../types";

export const ProfileSelectorPopover: React.FC<{
  sources: ProfileSourceItem[];
  editingSourceId: string;
  lang: "zh-CN" | "en";
  onSelectSource: (id: string) => void;
  onOpenModal: (mode: "blank" | "import" | "url" | "manage", target?: ProfileSourceItem) => void;
  onClose: () => void;
}> = ({ sources, editingSourceId, lang, onSelectSource, onOpenModal, onClose }) => {
  const en = lang === "en";
  const [hoveredId, setHoveredId] = useState<string | null>(null);

  return (
    <div
      onClick={(e) => e.stopPropagation()}
      style={{
        position: "absolute",
        top: 38,
        left: 0,
        zIndex: 70,
        minWidth: 220,
        maxWidth: 280,
        padding: 6,
        borderRadius: 11,
        background: "var(--elevated)",
        border: "0.75px solid rgba(var(--divider-rgb), 0.65)",
        boxShadow: "var(--popover-shadow)",
        display: "flex",
        flexDirection: "column",
        gap: 4,
      }}
    >
      <div
        style={{
          padding: "4px 8px 2px",
          fontSize: 11,
          fontWeight: 600,
          color: "var(--text-secondary)",
        }}
      >
        {en ? "Choose Configuration" : "选择配置"}
      </div>

      <div style={{ maxHeight: 220, overflowY: "auto", display: "flex", flexDirection: "column", gap: 2 }}>
        {sources.map((src) => {
          const active = src.id === editingSourceId;
          const hovered = hoveredId === src.id;
          return (
            <div
              key={src.id}
              onMouseEnter={() => setHoveredId(src.id)}
              onMouseLeave={() => setHoveredId(null)}
              onClick={() => {
                onSelectSource(src.id);
                onClose();
              }}
              style={{
                height: 30,
                padding: "0 8px",
                borderRadius: 7,
                display: "flex",
                alignItems: "center",
                gap: 7,
                background: hovered ? "rgba(var(--text-primary-rgb), 0.05)" : "transparent",
                cursor: "pointer",
              }}
            >
              {active ? (
                <Check size={13} color="var(--accent)" />
              ) : (
                <Circle size={12} color="var(--text-secondary)" />
              )}
              <span
                style={{
                  flex: 1,
                  fontSize: 12.5,
                  fontWeight: active ? 600 : 400,
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                  whiteSpace: "nowrap",
                }}
              >
                {src.name}
              </span>
              {hovered && (
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    onOpenModal("manage", src);
                    onClose();
                  }}
                  style={{
                    border: "none",
                    borderRadius: 999,
                    padding: "2px 7px",
                    fontSize: 10.5,
                    fontWeight: 500,
                    color: "var(--accent)",
                    background: "rgba(var(--accent-rgb), 0.12)",
                    cursor: "pointer",
                  }}
                >
                  {en ? "Edit" : "修改"}
                </button>
              )}
            </div>
          );
        })}
      </div>

      <div className="xd-divider" style={{ margin: "2px 0" }} />

      <div
        style={{
          padding: "2px 8px",
          fontSize: 11,
          fontWeight: 600,
          color: "var(--text-secondary)",
        }}
      >
        {en ? "Add Configuration" : "添加配置"}
      </div>

      <PopoverMenuBtn
        icon={<Plus size={13} />}
        label={en ? "New Blank Profile…" : "新建空白配置…"}
        onClick={() => {
          onOpenModal("blank");
          onClose();
        }}
      />
      <PopoverMenuBtn
        icon={<FileText size={13} />}
        label={en ? "Import File / Text…" : "从文件或文本导入…"}
        onClick={() => {
          onOpenModal("import");
          onClose();
        }}
      />
      <PopoverMenuBtn
        icon={<Link2 size={13} />}
        label={en ? "Subscribe from URL…" : "从链接订阅…"}
        onClick={() => {
          onOpenModal("url");
          onClose();
        }}
      />
    </div>
  );
};

const PopoverMenuBtn: React.FC<{
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
}> = ({ icon, label, onClick }) => (
  <button
    onClick={onClick}
    style={{
      height: 28,
      padding: "0 8px",
      borderRadius: 7,
      border: "none",
      background: "transparent",
      display: "flex",
      alignItems: "center",
      gap: 7,
      fontSize: 12,
      color: "var(--text-primary)",
      cursor: "pointer",
      textAlign: "left",
    }}
    onMouseEnter={(e) => {
      e.currentTarget.style.background = "rgba(var(--text-primary-rgb), 0.055)";
    }}
    onMouseLeave={(e) => {
      e.currentTarget.style.background = "transparent";
    }}
  >
    {icon}
    <span>{label}</span>
  </button>
);

export const ProfileManagerModal: React.FC<{
  mode: "blank" | "import" | "url" | "manage";
  target?: ProfileSourceItem;
  canDelete: boolean;
  lang: "zh-CN" | "en";
  onClose: () => void;
}> = ({ mode, target, canDelete, lang, onClose }) => {
  const en = lang === "en";
  const fileRef = useRef<HTMLInputElement | null>(null);
  const [name, setName] = useState(target?.name || "");
  const [url, setUrl] = useState(target?.remote_url || "");
  const [content, setContent] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [exportedJSON, setExportedJSON] = useState<string | null>(null);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setErr(null);
    try {
      await fn();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const handleFileChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    const text = await file.text();
    setContent(text);
    if (!name.trim()) {
      setName(file.name.replace(/\.(json|ya?ml|conf|txt)$/i, ""));
    }
  };

  return (
    <div
      onClick={onClose}
      style={{
        position: "fixed",
        inset: 0,
        zIndex: 100,
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
          width: mode === "manage" ? 460 : 430,
          maxHeight: "88vh",
          overflowY: "auto",
          borderRadius: 14,
          background: "var(--elevated)",
          border: "0.75px solid rgba(var(--divider-rgb), 0.65)",
          boxShadow: "var(--popover-shadow)",
          padding: 18,
          display: "flex",
          flexDirection: "column",
          gap: 12,
        }}
      >
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
          <span style={{ fontSize: 15, fontWeight: 700 }}>
            {mode === "blank"
              ? en
                ? "New Blank Profile"
                : "新建空白配置"
              : mode === "import"
                ? en
                  ? "Import Configuration"
                  : "导入配置（JSON / Clash / Surge / 节点链接）"
                : mode === "url"
                  ? en
                    ? "Subscribe from URL"
                    : "从链接导入订阅"
                  : en
                    ? "Manage Profile Source"
                    : `管理配置：${target?.name || ""}`}
          </span>
          <button
            onClick={onClose}
            style={{
              border: "none",
              background: "transparent",
              color: "var(--text-secondary)",
              cursor: "pointer",
            }}
          >
            <X size={16} />
          </button>
        </div>

        <div style={{ display: "flex", flexDirection: "column", gap: 5 }}>
          <label style={{ fontSize: 11.5, color: "var(--text-secondary)" }}>
            {en ? "Profile Name" : "配置名称"}
          </label>
          <input
            className="xd-input"
            value={name}
            placeholder={en ? "e.g. Office / Personal" : "例如：办公网络 / 机场订阅"}
            onChange={(e) => setName(e.target.value)}
          />
        </div>

        {mode === "url" && (
          <div style={{ display: "flex", flexDirection: "column", gap: 5 }}>
            <label style={{ fontSize: 11.5, color: "var(--text-secondary)" }}>
              {en ? "Subscription HTTPS URL" : "订阅链接（HTTPS）"}
            </label>
            <input
              className="xd-input"
              value={url}
              placeholder="https://..."
              onChange={(e) => setUrl(e.target.value)}
            />
          </div>
        )}

        {mode === "import" && (
          <>
            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
              <span style={{ fontSize: 11.5, color: "var(--text-secondary)" }}>
                {en ? "Configuration Content" : "配置内容（粘贴或选择本地文件）"}
              </span>
              <input
                ref={fileRef}
                type="file"
                accept=".json,.yaml,.yml,.conf,.txt"
                style={{ display: "none" }}
                onChange={handleFileChange}
              />
              <button
                className="xd-capsule-btn"
                style={{ height: 24 }}
                onClick={() => fileRef.current?.click()}
              >
                {en ? "Choose File…" : "选择文件…"}
              </button>
            </div>
            <textarea
              className="xd-textarea tabular-mono"
              rows={6}
              value={content}
              placeholder={
                en
                  ? "Paste XDial/sing-box JSON, Clash YAML, Surge conf, or ss:// / trojan:// / anytls:// links…"
                  : "支持粘贴 XDial / sing-box JSON、Clash YAML、Surge 配置或 Base64 节点链接…"
              }
              onChange={(e) => setContent(e.target.value)}
            />
          </>
        )}

        {mode === "manage" && target && (
          <>
            {target.is_subscription && (
              <div
                style={{
                  padding: 10,
                  borderRadius: 9,
                  background: "var(--surface)",
                  border: "0.75px solid rgba(var(--divider-rgb), 0.45)",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  gap: 8,
                }}
              >
                <div style={{ minWidth: 0, flex: 1 }}>
                  <div style={{ fontSize: 11, color: "var(--text-secondary)" }}>
                    {en ? "Subscription URL" : "在线订阅源"}
                  </div>
                  <div
                    className="selectable tabular-mono"
                    style={{
                      fontSize: 11,
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {target.remote_url}
                  </div>
                </div>
                <button
                  className="xd-capsule-btn"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      await rpc("refreshProfileSource", { source_id: target.id });
                      onClose();
                    })
                  }
                >
                  <RefreshCw size={12} className={busy ? "xd-spin" : undefined} />
                  <span>{en ? "Update" : "更新订阅"}</span>
                </button>
              </div>
            )}

            <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
              <button
                className="xd-capsule-btn"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    const out = await rpc<string>("exportProfileSource", {
                      source_id: target.id,
                    });
                    setExportedJSON(out);
                    await navigator.clipboard.writeText(out);
                  })
                }
              >
                <Download size={12} />
                <span>{en ? "Export & Copy JSON" : "导出并复制 JSON"}</span>
              </button>

              {canDelete && (
                <button
                  className="xd-capsule-btn danger"
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      await rpc("deleteProfileSource", { source_id: target.id });
                      onClose();
                    })
                  }
                >
                  <Trash2 size={12} />
                  <span>{en ? "Delete Source" : "删除此来源"}</span>
                </button>
              )}
            </div>

            {exportedJSON && (
              <div style={{ fontSize: 11, color: "var(--success)" }}>
                {en ? "Copied runtime JSON to clipboard." : "已将配置 JSON 复制到剪贴板。"}
              </div>
            )}
          </>
        )}

        {err && (
          <div className="selectable" style={{ fontSize: 11.5, color: "var(--danger)" }}>
            {err}
          </div>
        )}

        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8, marginTop: 4 }}>
          <button className="xd-capsule-btn" onClick={onClose}>
            {en ? "Cancel" : "取消"}
          </button>
          <button
            className="xd-capsule-btn primary"
            disabled={busy}
            onClick={() =>
              void run(async () => {
                if (mode === "blank") {
                  await rpc("createBlankProfile", { name });
                } else if (mode === "import") {
                  await rpc("importProfileContent", { name, content });
                } else if (mode === "url") {
                  await rpc("importSubscriptionURL", { name, url });
                } else if (mode === "manage" && target) {
                  await rpc("renameProfileSource", { source_id: target.id, name });
                }
                onClose();
              })
            }
          >
            {busy ? (en ? "Working…" : "处理中…") : en ? "Confirm" : "确认保存"}
          </button>
        </div>
      </div>
    </div>
  );
};
