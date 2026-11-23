import React, { useState } from "react";
import {
  ChevronDown,
  ChevronRight,
  Trash2,
  Plus,
  Search,
  ArrowLeftRight,
  Check,
  Copy,
  GripHorizontal,
  Wifi,
} from "lucide-react";
import { rpc } from "../../api/bridge";
import type { AppSnapshot, RuleBinding, RuleSet, RuleSetType, Scenario } from "../../types";
import {
  ScenarioIconGlyph,
  ScenarioIconPickerPopover,
} from "../../utils/scenarioCatalog";
import { describeScenario } from "../../utils/format";

// --- Rules Tab (DESIGN.md §5.3) ---

export const RulesTab: React.FC<{
  snap: AppSnapshot;
  onError: (msg: string | null) => void;
}> = ({ snap, onError }) => {
  const lang = snap.preferences.language || "zh-CN";
  const en = lang === "en";

  const [query, setQuery] = useState("");
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [showsAddMenu, setShowsAddMenu] = useState(false);

  const rules = snap.editor.rule_sets.filter(
    (r) => !query.trim() || r.name.toLowerCase().includes(query.trim().toLowerCase())
  );

  const exec = async (fn: () => Promise<unknown>) => {
    onError(null);
    try {
      await fn();
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e));
    }
  };

  const addNewRule = (type: RuleSetType) => {
    setShowsAddMenu(false);
    const base: RuleSet = {
      id: "",
      name: `${en ? "Rule" : "新规则"} ${snap.editor.rule_sets.length + 1}`,
      type,
      enabled: true,
      domains: type === "manual" ? ["example.com"] : undefined,
      format: type === "url" ? "srs" : undefined,
      fetch_line_id: type === "url" ? "direct" : undefined,
      processes: type === "application" ? ["chrome.exe"] : undefined,
    };
    void exec(() => rpc("saveRuleSet", base));
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div style={{ padding: "8px 16px", display: "flex", alignItems: "center", gap: 10 }}>
        <div style={{ position: "relative", flex: 1, maxWidth: 240 }}>
          <Search
            size={13}
            color="var(--text-secondary)"
            style={{ position: "absolute", left: 9, top: 8 }}
          />
          <input
            className="xd-input"
            style={{ width: "100%", paddingLeft: 28 }}
            placeholder={en ? "Search rules…" : "搜索规则…"}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
        <div style={{ flex: 1 }} />
        <span style={{ fontSize: 11.5, color: "var(--text-secondary)" }}>
          {rules.length} {en ? "rules" : "条规则"}
        </span>
      </div>

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
        {rules.map((rule) => {
          const isOpen = Boolean(expanded[rule.id]);
          const invertOn = Boolean(rule.invert);

          return (
            <div
              key={rule.id}
              className={`xd-card ${!rule.enabled ? "disabled-card" : ""}`}
              style={{ padding: "9px 11px" }}
            >
              <div
                onClick={() => setExpanded((p) => ({ ...p, [rule.id]: !p[rule.id] }))}
                style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer" }}
              >
                {isOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                <span style={{ fontSize: 13, fontWeight: 600 }}>{rule.name}</span>

                <span
                  style={{
                    fontSize: 11,
                    color: "var(--text-secondary)",
                    flex: 1,
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap",
                  }}
                >
                  {summarizeRule(rule, en)}
                </span>

                {/* Invert Pill Toggle (DESIGN.md §5.3) */}
                {(rule.type === "manual" || rule.type === "url" || rule.type === "application") && (
                  <button
                    onClick={(e) => {
                      e.stopPropagation();
                      void exec(() => rpc("saveRuleSet", { ...rule, invert: !invertOn }));
                    }}
                    style={{
                      height: 22,
                      padding: "0 8px",
                      borderRadius: 999,
                      fontSize: 10.5,
                      fontWeight: 500,
                      display: "inline-flex",
                      alignItems: "center",
                      gap: 4,
                      color: invertOn ? "var(--selection)" : "var(--text-secondary)",
                      background: invertOn
                        ? "rgba(var(--selection-rgb), 0.2)"
                        : "var(--surface)",
                      border: invertOn
                        ? "0.8px solid rgba(var(--selection-rgb), 0.72)"
                        : "0.75px solid rgba(var(--divider-rgb), 0.55)",
                      cursor: "pointer",
                    }}
                  >
                    {invertOn ? <Check size={11} /> : <ArrowLeftRight size={11} />}
                    <span>{en ? "Invert" : "反向"}</span>
                  </button>
                )}

                <button
                  type="button"
                  className="xd-switch compact"
                  data-checked={String(rule.enabled)}
                  onClick={(e) => {
                    e.stopPropagation();
                    void exec(() => rpc("saveRuleSet", { ...rule, enabled: !rule.enabled }));
                  }}
                >
                  <span className="xd-switch-thumb" />
                </button>

                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    void exec(() => rpc("deleteRuleSet", { id: rule.id }));
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
                <RuleEditorForm
                  rule={rule}
                  allLines={snap.editor.all_lines}
                  lang={lang}
                  onSave={(updated) => void exec(() => rpc("saveRuleSet", updated))}
                />
              )}
            </div>
          );
        })}
      </div>

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
        <button className="xd-capsule-btn" onClick={() => setShowsAddMenu((v) => !v)}>
          <Plus size={13} />
          <span>{en ? "Add Rule" : "添加规则"}</span>
        </button>

        {showsAddMenu && (
          <div
            style={{
              position: "absolute",
              bottom: 48,
              right: 16,
              zIndex: 60,
              minWidth: 190,
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
              onClick={() => addNewRule("manual")}
              style={menuItemStyle}
            >
              {en ? "Domains & IP CIDRs" : "手动规则（域名 / IP 网段）"}
            </button>
            <button
              onClick={() => addNewRule("url")}
              style={menuItemStyle}
            >
              {en ? "Remote RuleSet URL" : "远程规则集（SRS / JSON / Text）"}
            </button>
            <button
              onClick={() => addNewRule("application")}
              style={menuItemStyle}
            >
              {en ? "Process / Application" : "本地进程规则（.exe / 路径）"}
            </button>
          </div>
        )}
      </div>
    </div>
  );
};

function summarizeRule(r: RuleSet, en: boolean): string {
  if (r.type === "url") return r.url || (en ? "Remote RuleSet" : "远程规则集");
  if (r.type === "application") {
    return (r.processes || []).join(", ") || (en ? "Process Rule" : "应用进程规则");
  }
  const dCount = r.domains?.length || 0;
  const cCount = r.cidrs?.length || 0;
  if (en) return `${dCount} domains · ${cCount} CIDRs`;
  return `${dCount} 域名 · ${cCount} 网段`;
}

const RuleEditorForm: React.FC<{
  rule: RuleSet;
  allLines: AppSnapshot["editor"]["all_lines"];
  lang: "zh-CN" | "en";
  onSave: (r: RuleSet) => void;
}> = ({ rule, allLines, lang, onSave }) => {
  const en = lang === "en";
  const [name, setName] = useState(rule.name);
  const [domainsText, setDomainsText] = useState((rule.domains || []).join("\n"));
  const [cidrsText, setCidrsText] = useState((rule.cidrs || []).join("\n"));
  const [procText, setProcText] = useState((rule.processes || []).join("\n"));
  const [url, setUrl] = useState(rule.url || "");
  const [format, setFormat] = useState(rule.format || "auto");
  const [fetchLine, setFetchLine] = useState(rule.fetch_line_id || "direct");

  const splitLines = (s: string) =>
    s
      .split(/[\r\n,]+/)
      .map((x) => x.trim())
      .filter(Boolean);

  const commit = (override?: Partial<RuleSet>) => {
    onSave({
      ...rule,
      name: name.trim() || rule.name,
      domains: rule.type === "manual" ? splitLines(domainsText) : rule.domains,
      cidrs: rule.type === "manual" ? splitLines(cidrsText) : rule.cidrs,
      processes: rule.type === "application" ? splitLines(procText) : rule.processes,
      url: rule.type === "url" ? url.trim() : rule.url,
      format: rule.type === "url" ? format : rule.format,
      fetch_line_id: rule.type === "url" ? fetchLine : rule.fetch_line_id,
      ...override,
    });
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
      <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        <label style={{ fontSize: 11, color: "var(--text-secondary)" }}>
          {en ? "Rule Name" : "规则名称"}
        </label>
        <input
          className="xd-input"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onBlur={() => commit()}
        />
      </div>

      {rule.type === "manual" && (
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
          <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
            <label style={{ fontSize: 11, color: "var(--text-secondary)" }}>
              {en ? "Domains (one per line)" : "域名后缀 / 域名（每行一条）"}
            </label>
            <textarea
              className="xd-textarea tabular-mono"
              rows={4}
              placeholder="corp.example.com&#10;anthropic.com"
              value={domainsText}
              onChange={(e) => setDomainsText(e.target.value)}
              onBlur={() => commit()}
            />
          </div>
          <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
            <label style={{ fontSize: 11, color: "var(--text-secondary)" }}>
              {en ? "IP CIDRs (one per line)" : "IP CIDR 网段（每行一条）"}
            </label>
            <textarea
              className="xd-textarea tabular-mono"
              rows={4}
              placeholder="10.0.0.0/8&#10;192.168.0.0/16"
              value={cidrsText}
              onChange={(e) => setCidrsText(e.target.value)}
              onBlur={() => commit()}
            />
          </div>
        </div>
      )}

      {rule.type === "url" && (
        <>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 150px", gap: 8 }}>
            <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
              <label style={{ fontSize: 11, color: "var(--text-secondary)" }}>
                {en ? "Remote HTTPS URL" : "远程规则集地址（HTTPS）"}
              </label>
              <input
                className="xd-input tabular-mono"
                placeholder="https://..."
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                onBlur={() => commit()}
              />
            </div>
            <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
              <label style={{ fontSize: 11, color: "var(--text-secondary)" }}>
                {en ? "Fetch Via Line" : "下载获取线路"}
              </label>
              <select
                className="xd-select"
                value={fetchLine}
                onChange={(e) => {
                  setFetchLine(e.target.value);
                  commit({ fetch_line_id: e.target.value });
                }}
              >
                <option value="direct">{en ? "Direct" : "直连"}</option>
                {allLines
                  .filter((l) => l.id !== "direct" && !l.type.includes("vpn") && l.enabled)
                  .map((l) => (
                    <option key={l.id} value={l.id}>
                      {l.name}
                    </option>
                  ))}
              </select>
            </div>
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <span style={{ fontSize: 11, color: "var(--text-secondary)" }}>
              {en ? "Format:" : "文件格式："}
            </span>
            <select
              className="xd-select"
              value={format}
              onChange={(e) => {
                setFormat(e.target.value);
                commit({ format: e.target.value });
              }}
            >
              <option value="auto">auto</option>
              <option value="srs">srs (binary)</option>
              <option value="json">json (source)</option>
              <option value="text">text</option>
            </select>
          </div>
        </>
      )}

      {rule.type === "application" && (
        <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
          <label style={{ fontSize: 11, color: "var(--text-secondary)" }}>
            {en
              ? "Process Names or Executable Paths (one per line)"
              : "进程文件名或可执行文件完整路径（每行一项，如 cursor.exe、claude.exe）"}
          </label>
          <textarea
            className="xd-textarea tabular-mono"
            rows={4}
            placeholder="cursor.exe&#10;chrome.exe"
            value={procText}
            onChange={(e) => setProcText(e.target.value)}
            onBlur={() => commit()}
          />
        </div>
      )}
    </div>
  );
};

// --- Scenarios Tab (DESIGN.md §5.4 & §5.5) ---

export const ScenariosTab: React.FC<{
  snap: AppSnapshot;
  onError: (msg: string | null) => void;
}> = ({ snap, onError }) => {
  const lang = snap.preferences.language || "zh-CN";
  const en = lang === "en";

  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [iconPickerFor, setIconPickerFor] = useState<string | null>(null);
  const [newSsidText, setNewSsidText] = useState("");
  const [dragBindingIdx, setDragBindingIdx] = useState<number | null>(null);

  const scenarios = snap.editor.scenarios;
  const allLines = snap.editor.all_lines.filter((l) => l.enabled || l.id === "direct");
  const allRules = snap.editor.all_rule_sets.filter((r) => r.enabled);
  const currentWifi = snap.system_facts.connected_ssid;

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
        {scenarios.map((sc) => {
          const isActive = sc.id === snap.editor.active_scenario_id;
          const isOpen = expandedId === sc.id;
          const summary = describeScenario(sc, snap.editor.all_lines, snap.editor.all_rule_sets, lang);

          return (
            <div
              key={sc.id}
              className={`xd-card ${isActive ? "active-scenario-card" : ""}`}
              style={{ padding: "9px 11px 9px 13px" }}
            >
              {/* Header Row */}
              <div
                onClick={() => setExpandedId(isOpen ? null : sc.id)}
                style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer" }}
              >
                {isOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}

                <span style={{ color: isActive ? "var(--selection)" : "var(--accent)", display: "inline-flex" }}>
                  <ScenarioIconGlyph
                    iconOverride={sc.icon}
                    name={sc.name}
                    matchSSIDs={sc.match_ssids}
                    size={16}
                  />
                </span>

                <span style={{ fontSize: 13, fontWeight: 600 }}>{sc.name}</span>

                {/* SSID Capsules in Header */}
                {(sc.match_ssids || []).map((ssid) => (
                  <span
                    key={ssid}
                    style={{
                      height: 20,
                      padding: "0 7px",
                      borderRadius: 999,
                      fontSize: 10.5,
                      background: "rgba(var(--accent-rgb), 0.11)",
                      color: "var(--accent)",
                      display: "inline-flex",
                      alignItems: "center",
                      gap: 4,
                      maxWidth: 140,
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    <Wifi size={10} />
                    <span>{ssid}</span>
                  </span>
                ))}

                <span
                  style={{
                    fontSize: 11.5,
                    color: "var(--text-secondary)",
                    flex: 1,
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap",
                  }}
                >
                  {summary}
                </span>

                {!isActive && (
                  <button
                    className="xd-capsule-btn"
                    style={{ height: 22, padding: "0 8px", fontSize: 10.5 }}
                    onClick={(e) => {
                      e.stopPropagation();
                      void exec(() => rpc("selectScenario", { id: sc.id }));
                    }}
                  >
                    {en ? "Use" : "选用"}
                  </button>
                )}

                <button
                  title={en ? "Duplicate Scenario" : "复制场景"}
                  onClick={(e) => {
                    e.stopPropagation();
                    void exec(() => rpc("duplicateScenario", { id: sc.id }));
                  }}
                  style={iconActionStyle}
                >
                  <Copy size={13} />
                </button>

                {scenarios.length > 1 && !isActive && (
                  <button
                    title={en ? "Delete Scenario" : "删除场景"}
                    onClick={(e) => {
                      e.stopPropagation();
                      void exec(() => rpc("deleteScenario", { id: sc.id }));
                    }}
                    style={iconActionStyle}
                  >
                    <Trash2 size={13} />
                  </button>
                )}
              </div>

              {isOpen && (
                <div
                  onClick={(e) => e.stopPropagation()}
                  style={{
                    padding: "11px 4px 4px 20px",
                    display: "flex",
                    flexDirection: "column",
                    gap: 10,
                  }}
                >
                  {/* Name + Icon Picker */}
                  <div style={{ display: "flex", gap: 8, alignItems: "center", position: "relative" }}>
                    <input
                      className="xd-input"
                      style={{ flex: 1 }}
                      defaultValue={sc.name}
                      onBlur={(e) => {
                        const v = e.target.value.trim();
                        if (v && v !== sc.name) {
                          void exec(() => rpc("saveScenario", { ...sc, name: v }));
                        }
                      }}
                    />
                    <button
                      onClick={() =>
                        setIconPickerFor(iconPickerFor === sc.id ? null : sc.id)
                      }
                      style={{
                        width: 32,
                        height: 28,
                        borderRadius: 7,
                        border: "0.75px solid rgba(var(--divider-rgb), 0.65)",
                        background: "var(--canvas)",
                        color: "var(--accent)",
                        display: "flex",
                        alignItems: "center",
                        justifyContent: "center",
                        cursor: "pointer",
                      }}
                    >
                      <ScenarioIconGlyph
                        iconOverride={sc.icon}
                        name={sc.name}
                        matchSSIDs={sc.match_ssids}
                        size={15}
                      />
                    </button>

                    {iconPickerFor === sc.id && (
                      <ScenarioIconPickerPopover
                        currentOverride={sc.icon}
                        scenarioName={sc.name}
                        matchSSIDs={sc.match_ssids}
                        lang={lang}
                        onSelect={(key) =>
                          void exec(() => rpc("saveScenario", { ...sc, icon: key }))
                        }
                        onClose={() => setIconPickerFor(null)}
                      />
                    )}
                  </div>

                  {/* Wi-Fi SSID Automatic Matching */}
                  <div style={{ display: "flex", flexDirection: "column", gap: 5 }}>
                    <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
                      <span style={{ fontSize: 11, color: "var(--text-secondary)" }}>
                        {en ? "Auto-activate on Wi-Fi SSID" : "连上指定 Wi-Fi 时自动选用此场景"}
                      </span>
                      {currentWifi && !(sc.match_ssids || []).includes(currentWifi) && (
                        <button
                          className="xd-capsule-btn"
                          style={{ height: 22, fontSize: 10.5 }}
                          onClick={() =>
                            void exec(() =>
                              rpc("saveScenario", {
                                ...sc,
                                match_ssids: [...(sc.match_ssids || []), currentWifi],
                              })
                            )
                          }
                        >
                          <Wifi size={10} />
                          <span>
                            {en ? `Add “${currentWifi}”` : `添加当前 Wi-Fi「${currentWifi}」`}
                          </span>
                        </button>
                      )}
                    </div>

                    <div style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center" }}>
                      {(sc.match_ssids || []).map((ssid) => (
                        <span
                          key={ssid}
                          style={{
                            height: 24,
                            padding: "0 8px",
                            borderRadius: 999,
                            background: "rgba(var(--accent-rgb), 0.12)",
                            color: "var(--accent)",
                            fontSize: 11.5,
                            display: "inline-flex",
                            alignItems: "center",
                            gap: 5,
                          }}
                        >
                          <span>{ssid}</span>
                          <button
                            onClick={() =>
                              void exec(() =>
                                rpc("saveScenario", {
                                  ...sc,
                                  match_ssids: (sc.match_ssids || []).filter((x) => x !== ssid),
                                })
                              )
                            }
                            style={{
                              border: "none",
                              background: "transparent",
                              color: "var(--accent)",
                              cursor: "pointer",
                              fontSize: 11,
                            }}
                          >
                            ✕
                          </button>
                        </span>
                      ))}

                      <input
                        className="xd-input"
                        style={{ width: 150, height: 25, fontSize: 11.5 }}
                        placeholder={en ? "Enter SSID + Enter" : "输入 SSID 回车添加"}
                        value={newSsidText}
                        onChange={(e) => setNewSsidText(e.target.value)}
                        onKeyDown={(e) => {
                          if (e.key === "Enter" && newSsidText.trim()) {
                            const val = newSsidText.trim();
                            setNewSsidText("");
                            void exec(() =>
                              rpc("saveScenario", {
                                ...sc,
                                match_ssids: Array.from(new Set([...(sc.match_ssids || []), val])),
                              })
                            );
                          }
                        }}
                      />
                    </div>
                  </div>

                  {/* Ordered RuleSet -> Line Bindings (Drag handle on leading 28x28 slot) */}
                  <div style={{ display: "flex", flexDirection: "column", gap: 5 }}>
                    <span style={{ fontSize: 11, color: "var(--text-secondary)" }}>
                      {en ? "Routing Rules (evaluated top to bottom)" : "分流规则（按从上到下顺序优先匹配）"}
                    </span>

                    <div
                      style={{
                        border: "0.75px solid rgba(var(--divider-rgb), 0.45)",
                        borderRadius: 8,
                        background: "var(--canvas)",
                        overflow: "hidden",
                      }}
                    >
                      {(sc.bindings || []).map((b, idx) => {
                        const rObj = snap.editor.all_rule_sets.find((r) => r.id === b.rule_set_id);
                        return (
                          <div
                            key={`${b.rule_set_id}-${idx}`}
                            draggable={(sc.bindings || []).length > 1}
                            onDragStart={() => setDragBindingIdx(idx)}
                            onDragEnd={() => setDragBindingIdx(null)}
                            onDragOver={(e) => e.preventDefault()}
                            onDrop={() => {
                              if (dragBindingIdx === null || dragBindingIdx === idx) return;
                              const next: RuleBinding[] = [...sc.bindings];
                              const [moved] = next.splice(dragBindingIdx, 1);
                              next.splice(idx, 0, moved);
                              setDragBindingIdx(null);
                              void exec(() => rpc("saveScenario", { ...sc, bindings: next }));
                            }}
                            style={{
                              minHeight: 34,
                              padding: "4px 8px 4px 4px",
                              display: "flex",
                              alignItems: "center",
                              gap: 8,
                              borderBottom: "0.75px solid rgba(var(--divider-rgb), 0.25)",
                              opacity: dragBindingIdx === idx ? 0.25 : 1,
                            }}
                          >
                            <span
                              style={{
                                width: 28,
                                height: 28,
                                display: "inline-flex",
                                alignItems: "center",
                                justifyContent: "center",
                                color: "var(--text-secondary)",
                                cursor: (sc.bindings || []).length > 1 ? "grab" : "default",
                              }}
                            >
                              <GripHorizontal size={13} />
                            </span>

                            <span style={{ flex: 1, fontSize: 12.5, fontWeight: 500 }}>
                              {rObj?.name || b.rule_set_id}
                            </span>

                            <select
                              className="xd-select"
                              style={{ width: 155 }}
                              value={b.line_id || "direct"}
                              onChange={(e) => {
                                const next = [...sc.bindings];
                                next[idx] = { ...b, line_id: e.target.value };
                                void exec(() => rpc("saveScenario", { ...sc, bindings: next }));
                              }}
                            >
                              <option value="direct">{en ? "Direct" : "直连"}</option>
                              {allLines
                                .filter((l) => l.id !== "direct")
                                .map((l) => (
                                  <option key={l.id} value={l.id}>
                                    {l.name}
                                  </option>
                                ))}
                            </select>

                            <button
                              onClick={() => {
                                const next = sc.bindings.filter((_, i) => i !== idx);
                                void exec(() => rpc("saveScenario", { ...sc, bindings: next }));
                              }}
                              style={iconActionStyle}
                            >
                              ✕
                            </button>
                          </div>
                        );
                      })}

                      {/* Default Exit ("其他流量") Row */}
                      <div
                        style={{
                          minHeight: 34,
                          padding: "4px 8px 4px 32px",
                          display: "flex",
                          alignItems: "center",
                          gap: 8,
                          background: "rgba(var(--text-primary-rgb), 0.02)",
                        }}
                      >
                        <span style={{ flex: 1, fontSize: 12.5, fontWeight: 600 }}>
                          {en ? "Other Traffic (Default)" : "其他流量（默认出口）"}
                        </span>
                        <select
                          className="xd-select"
                          style={{ width: 155 }}
                          value={sc.default_line_id || "direct"}
                          onChange={(e) =>
                            void exec(() =>
                              rpc("saveScenario", { ...sc, default_line_id: e.target.value })
                            )
                          }
                        >
                          <option value="direct">{en ? "Direct" : "直连"}</option>
                          {allLines
                            .filter((l) => l.id !== "direct")
                            .map((l) => (
                              <option key={l.id} value={l.id}>
                                {l.name}
                              </option>
                            ))}
                        </select>
                        <span style={{ width: 19 }} />
                      </div>
                    </div>

                    {/* Add Rule Binding Selector */}
                    {allRules.filter((r) => !(sc.bindings || []).some((b) => b.rule_set_id === r.id))
                      .length > 0 && (
                      <div style={{ display: "flex", flexWrap: "wrap", gap: 6, marginTop: 3 }}>
                        {allRules
                          .filter((r) => !(sc.bindings || []).some((b) => b.rule_set_id === r.id))
                          .map((r) => (
                            <button
                              key={r.id}
                              className="xd-capsule-btn"
                              style={{ height: 24 }}
                              onClick={() =>
                                void exec(() =>
                                  rpc("saveScenario", {
                                    ...sc,
                                    bindings: [
                                      ...(sc.bindings || []),
                                      { rule_set_id: r.id, line_id: "direct" },
                                    ],
                                  })
                                )
                              }
                            >
                              <Plus size={11} />
                              <span>{r.name}</span>
                            </button>
                          ))}
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>
          );
        })}
      </div>

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
        <button
          className="xd-capsule-btn"
          onClick={() =>
            void exec(() =>
              rpc("saveScenario", {
                id: "",
                name: `${en ? "Scenario" : "新场景"} ${scenarios.length + 1}`,
                default_line_id: "direct",
                bindings: [],
              } as Scenario)
            )
          }
        >
          <Plus size={13} />
          <span>{en ? "Add Scenario" : "添加场景"}</span>
        </button>
      </div>
    </div>
  );
};

const menuItemStyle: React.CSSProperties = {
  border: "none",
  background: "transparent",
  padding: "6px 10px",
  borderRadius: 6,
  textAlign: "left",
  fontSize: 12,
  color: "var(--text-primary)",
  cursor: "pointer",
};

const iconActionStyle: React.CSSProperties = {
  border: "none",
  background: "transparent",
  color: "var(--text-secondary)",
  cursor: "pointer",
  padding: 3,
  display: "inline-flex",
};
