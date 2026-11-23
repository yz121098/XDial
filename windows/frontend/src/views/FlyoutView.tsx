import React, { useEffect, useRef, useState } from "react";
import {
  Settings,
  MoreHorizontal,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  ArrowDown,
  ArrowUp,
  ArrowRight,
  CheckCircle2,
  Circle,
  AlertTriangle,
  AlertCircle,
  Plug,
  Copy,
  Check,
  RotateCw,
  UserCircle2,
  X,
} from "lucide-react";
import { rpc } from "../api/bridge";
import type { AppSnapshot, Scenario, TaskState } from "../types";
import { RotaryDial } from "../components/RotaryDial";
import { LatencyBadge } from "../components/LatencyBadge";
import { SignInSheet } from "../components/SignInSheet";
import { ScenarioIconGlyph } from "../utils/scenarioCatalog";
import { describeScenario, formatDuration, formatRate } from "../utils/format";

function taskMeta(
  state: TaskState,
  lang: "zh-CN" | "en"
): { label: string; color: string; completed: boolean } {
  const en = lang === "en";
  switch (state) {
    case "ready":
      return { label: en ? "Ready" : "就绪", color: "var(--success)", completed: true };
    case "committed":
      return { label: en ? "Committed" : "已提交", color: "var(--success)", completed: true };
    case "running":
      return { label: en ? "Running" : "进行中", color: "var(--accent)", completed: false };
    case "committing":
      return { label: en ? "Committing" : "提交中", color: "var(--accent)", completed: false };
    case "failed":
      return { label: en ? "Failed" : "失败", color: "var(--danger)", completed: false };
    case "rolling_back":
      return { label: en ? "Rolling back" : "回滚中", color: "var(--text-secondary)", completed: false };
    case "rolled_back":
      return { label: en ? "Rolled back" : "已回滚", color: "var(--text-secondary)", completed: true };
    case "skipped":
      return { label: en ? "Skipped" : "已跳过", color: "var(--text-secondary)", completed: true };
    default:
      return { label: en ? "Waiting" : "等待", color: "var(--text-secondary)", completed: false };
  }
}

export const FlyoutView: React.FC<{ snap: AppSnapshot }> = ({ snap }) => {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const lang = snap.preferences.language || "zh-CN";
  const en = lang === "en";

  const [nowMs, setNowMs] = useState(Date.now());
  const [showsScenarios, setShowsScenarios] = useState(false);
  const [showsDetails, setShowsDetails] = useState(false);
  const [showsPolicyMappings, setShowsPolicyMappings] = useState(false);
  const [showsMoreMenu, setShowsMoreMenu] = useState(false);
  const [pendingSwitch, setPendingSwitch] = useState<Scenario | null>(null);
  const [copiedAddr, setCopiedAddr] = useState(false);
  const [showsSignIn, setShowsSignIn] = useState<boolean>(
    Boolean(snap.needs_sign_in || snap.fresh_unconfigured)
  );
  const autoPromptedRef = useRef<boolean>(
    Boolean(snap.needs_sign_in || snap.fresh_unconfigured)
  );
  const [toastErr, setToastErr] = useState<string | null>(null);

  const isConnected = snap.phase === "connected";
  const isBusy =
    snap.phase === "connecting" ||
    snap.phase === "switching" ||
    snap.phase === "disconnecting";
  const isFailed = snap.phase === "failed";
  const connectedColor = snap.is_proxy_only ? "var(--proxy-only)" : "var(--success)";

  useEffect(() => {
    if (
      !autoPromptedRef.current &&
      snap.phase === "disconnected" &&
      (snap.needs_sign_in || snap.fresh_unconfigured)
    ) {
      autoPromptedRef.current = true;
      setShowsSignIn(true);
    } else if (
      showsSignIn &&
      !snap.needs_sign_in &&
      !snap.fresh_unconfigured &&
      (snap.phase === "connecting" || snap.phase === "connected")
    ) {
      setShowsSignIn(false);
    }
  }, [snap.phase, snap.needs_sign_in, snap.fresh_unconfigured, showsSignIn]);

  useEffect(() => {
    if (!isConnected || !snap.connected_at) return;
    const id = window.setInterval(() => setNowMs(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [isConnected, snap.connected_at]);

  // Keyboard shortcuts inside Flyout: Escape hides, Ctrl+, opens Settings
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        if (showsMoreMenu) setShowsMoreMenu(false);
        else if (showsSignIn) {
          setShowsSignIn(false);
          if (snap.fresh_unconfigured) void rpc("dismissOnboarding");
        }
        else if (pendingSwitch) setPendingSwitch(null);
        else void rpc("hideFlyout");
      } else if ((e.ctrlKey || e.metaKey) && e.key === ",") {
        e.preventDefault();
        void rpc("openSettings", {});
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [showsMoreMenu, showsSignIn, pendingSwitch, snap.fresh_unconfigured]);

  // Sync Flyout dynamic height to Wails window
  useEffect(() => {
    if (showsSignIn) {
      void rpc("setFlyoutHeight", { height: 490 });
      return;
    }
    const el = containerRef.current;
    if (!el) return;
    const syncHeight = () => {
      const h = Math.ceil(el.scrollHeight);
      if (h > 0) {
        void rpc("setFlyoutHeight", { height: Math.min(720, Math.max(320, h + 2)) });
      }
    };
    syncHeight();
    const ro = new ResizeObserver(syncHeight);
    ro.observe(el);
    return () => ro.disconnect();
  }, [showsSignIn]);

  const activeScenario =
    snap.editor.scenarios.find((s) => s.id === snap.editor.active_scenario_id) ||
    snap.editor.scenarios[0];

  const activeVpnLine = snap.editor.all_lines.find(
    (l) => l.type === "vpn" && l.enabled
  );

  const runAction = async (fn: () => Promise<unknown>) => {
    setToastErr(null);
    try {
      await fn();
    } catch (err) {
      setToastErr(err instanceof Error ? err.message : String(err));
    }
  };

  const handleDialClick = () => {
    if (snap.phase === "disconnecting") return;
    if (snap.phase === "connecting" || snap.phase === "switching") {
      void runAction(() => rpc("cancelConnect"));
      return;
    }
    if (snap.phase === "connected") {
      void runAction(() => rpc("disconnect"));
      return;
    }
    if (snap.needs_sign_in) {
      setShowsSignIn(true);
      return;
    }
    void runAction(() => rpc("connect"));
  };

  const handleScenarioClick = (sc: Scenario) => {
    if (snap.phase === "disconnecting") return;
    if (sc.id === snap.editor.active_scenario_id) {
      setPendingSwitch(null);
      return;
    }
    if (isConnected || snap.phase === "connecting" || snap.phase === "switching") {
      setPendingSwitch(sc);
      return;
    }
    void runAction(() => rpc("selectScenario", { id: sc.id }));
  };

  // Hero Dial copy
  let dialTitle = en ? "Disconnected" : "未连接";
  let dialHint = en ? "Click the dial to connect" : "点按拨盘连接";
  if (snap.phase === "connecting") {
    dialTitle = en ? "Connecting" : "正在连接";
    dialHint = en ? "Click the dial to cancel" : "点按拨盘取消";
  } else if (snap.phase === "switching") {
    dialTitle = en ? "Switching" : "切换中";
    dialHint = `${snap.target_scenario_name || ""} · ${en ? "Click to cancel" : "点按取消"}`;
  } else if (snap.phase === "disconnecting") {
    dialTitle = en ? "Disconnecting…" : "正在断开…";
    dialHint = en ? "Restoring network settings" : "正在恢复系统网络设置";
  } else if (isConnected) {
    dialTitle = activeScenario?.name || (en ? "Connected" : "已连接");
    dialHint = en ? "Click the dial to disconnect" : "点按拨盘断开";
  } else if (isFailed) {
    dialTitle = en ? "Connection Failed" : "连接失败";
    dialHint = en ? "Click the dial to retry" : "点按拨盘重试";
  } else if (snap.needs_sign_in) {
    dialTitle = en ? "Not Signed In" : "还没登录";
    dialHint = en ? "Sign in with your account to connect" : "登录公司账号后就能连接";
  }

  const tasks = snap.tasks || [];
  const completedTasks = tasks.filter((t) => taskMeta(t.state, lang).completed).length;

  const socksEndpoint = `127.0.0.1:${snap.active_socks_port || snap.preferences.socks_port}`;

  return (
    <div
      style={{
        width: "100%",
        height: "100%",
        overflowY: "auto",
        overflowX: "hidden",
        background: "var(--canvas)",
        border: "0.75px solid rgba(var(--divider-rgb), 0.45)",
      }}
      onClick={() => {
        if (showsMoreMenu) setShowsMoreMenu(false);
      }}
    >
      <div ref={containerRef} style={{ display: "flex", flexDirection: "column" }}>
        {/* 1. Header (54px) */}
        <div
          style={{
            minHeight: 54,
            padding: "0 18px",
            display: "flex",
            alignItems: "center",
            gap: 10,
            position: "relative",
          }}
        >
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <span style={{ fontSize: 19, fontWeight: 700, letterSpacing: "-0.01em" }}>
              XDial
            </span>

            {isConnected && snap.connected_at && (
              <div
                style={{
                  display: "inline-flex",
                  alignItems: "center",
                  gap: 6,
                  padding: "3px 8px",
                  borderRadius: 999,
                  background: snap.is_proxy_only
                    ? "rgba(var(--proxy-only-rgb), 0.13)"
                    : "rgba(var(--success-rgb), 0.13)",
                  color: connectedColor,
                  fontSize: 12,
                  fontWeight: 600,
                }}
              >
                <span
                  style={{
                    width: 4,
                    height: 4,
                    borderRadius: "50%",
                    background: connectedColor,
                  }}
                />
                <span className="tabular-mono">
                  {formatDuration(snap.connected_at, nowMs)}
                </span>
              </div>
            )}

            {snap.is_proxy_only && (
              <button
                onClick={() => void rpc("openSettings", { section: "startup" })}
                style={{
                  height: 20,
                  padding: "0 7px",
                  borderRadius: 999,
                  fontSize: 10.5,
                  fontWeight: 600,
                  color: "var(--proxy-only)",
                  background: "rgba(var(--proxy-only-rgb), 0.12)",
                  border: "0.75px solid rgba(var(--proxy-only-rgb), 0.34)",
                  cursor: "pointer",
                }}
              >
                {en ? "SOCKS5 only" : "仅 SOCKS5"}
              </button>
            )}
          </div>

          <div style={{ flex: 1 }} />

          <button
            className="xd-icon-circle-btn"
            title={en ? "Settings (Ctrl+,)" : "设置 (Ctrl+,)"}
            onClick={() => void rpc("openSettings", {})}
            style={{ color: "var(--accent)" }}
          >
            <Settings size={14} strokeWidth={2.1} />
          </button>

          <button
            className="xd-icon-circle-btn"
            title={en ? "More Actions" : "更多操作"}
            onClick={(e) => {
              e.stopPropagation();
              setShowsMoreMenu((v) => !v);
            }}
          >
            <MoreHorizontal size={15} strokeWidth={2.1} />
          </button>

          {showsMoreMenu && (
            <div
              onClick={(e) => e.stopPropagation()}
              style={{
                position: "absolute",
                top: 48,
                right: 16,
                zIndex: 50,
                minWidth: 168,
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
              <MenuActionBtn
                label={en ? "VPN Account Sign-In…" : "VPN 账号与登录…"}
                onClick={() => {
                  setShowsMoreMenu(false);
                  setShowsSignIn(true);
                }}
              />
              <MenuActionBtn
                label={en ? "System & Permissions…" : "系统与权限状态…"}
                onClick={() => {
                  setShowsMoreMenu(false);
                  void rpc("openSettings", { section: "system" });
                }}
              />
              <MenuActionBtn
                label={en ? "Copy Diagnostics JSON" : "复制结构化诊断信息"}
                onClick={async () => {
                  setShowsMoreMenu(false);
                  const diag = await rpc<string>("exportDiagnostics");
                  await navigator.clipboard.writeText(diag);
                }}
              />
              <MenuActionBtn
                label={en ? "About & Updates…" : "关于与更新…"}
                onClick={() => {
                  setShowsMoreMenu(false);
                  void rpc("openSettings", { section: "about" });
                }}
              />
              <div className="xd-divider" style={{ margin: "3px 0" }} />
              <MenuActionBtn
                label={en ? "Quit XDial" : "退出 XDial"}
                danger
                onClick={() => void rpc("quitApp")}
              />
            </div>
          )}
        </div>

        {/* 2. Home Controls */}
        <div
          style={{
            padding: "0 14px 14px 14px",
            display: "flex",
            flexDirection: "column",
            gap: 13,
          }}
        >
          {/* Hero Dial Instrument Surface Card */}
          <div
            className="dial-instrument-surface"
            style={{
              padding: "15px 17px",
              display: "flex",
              alignItems: "center",
              justifyContent: "space-between",
              gap: 10,
            }}
          >
            <div style={{ display: "flex", flexDirection: "column", gap: 6, minWidth: 0 }}>
              <div
                style={{
                  fontSize: 21,
                  fontWeight: 700,
                  color: "#FFFFFF",
                  lineHeight: 1.2,
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                  whiteSpace: "nowrap",
                }}
              >
                {dialTitle}
              </div>
              <div style={{ fontSize: 12.5, color: "rgba(255, 255, 255, 0.84)" }}>
                {dialHint}
              </div>
              {activeScenario && !isConnected && snap.phase === "disconnected" && (
                <div
                  style={{
                    marginTop: 2,
                    display: "inline-flex",
                    alignItems: "center",
                    gap: 5,
                    fontSize: 11.5,
                    color: "rgba(255, 255, 255, 0.72)",
                  }}
                >
                  <ScenarioIconGlyph
                    iconOverride={activeScenario.icon}
                    name={activeScenario.name}
                    matchSSIDs={activeScenario.match_ssids}
                    size={13}
                  />
                  <span>{activeScenario.name}</span>
                </div>
              )}
            </div>

            <RotaryDial
              phase={snap.phase}
              isProxyOnly={snap.is_proxy_only}
              disabled={snap.phase === "disconnecting" || Boolean(snap.blocker_message && snap.phase === "disconnected")}
              onTrigger={handleDialClick}
            />
          </div>

          {toastErr && (
            <div
              className="xd-rail-danger selectable"
              style={{
                padding: "8px 11px",
                borderRadius: 9,
                border: "0.75px solid rgba(var(--danger-rgb), 0.35)",
                color: "var(--danger)",
                fontSize: 11.5,
                display: "flex",
                justifyContent: "space-between",
                gap: 8,
              }}
            >
              <span>{toastErr}</span>
              <button
                onClick={() => setToastErr(null)}
                style={{
                  border: "none",
                  background: "transparent",
                  color: "var(--danger)",
                  cursor: "pointer",
                }}
              >
                ✕
              </button>
            </div>
          )}

          {/* Routing / Scenarios Section */}
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            <div
              style={{
                display: "flex",
                alignItems: "center",
                justifyContent: "space-between",
                padding: "0 4px",
              }}
            >
              <span style={{ fontSize: 12.5, fontWeight: 600 }}>
                {en ? "Routing" : "上网方式"}
              </span>
              <label
                style={{
                  display: "inline-flex",
                  alignItems: "center",
                  gap: 6,
                  fontSize: 11.5,
                  color: "var(--text-secondary)",
                  cursor: "pointer",
                }}
              >
                <span>{en ? "Automatic" : "自动"}</span>
                <button
                  type="button"
                  className="xd-switch compact"
                  data-checked={String(snap.preferences.automatic_scenario)}
                  onClick={() =>
                    void runAction(() =>
                      rpc("setAutomaticScenario", {
                        enabled: !snap.preferences.automatic_scenario,
                      })
                    )
                  }
                >
                  <span className="xd-switch-thumb" />
                </button>
              </label>
            </div>

            {snap.preferences.automatic_scenario && activeScenario && (
              <button
                onClick={() => setShowsScenarios((v) => !v)}
                style={{
                  padding: "11px 13px",
                  borderRadius: 14,
                  background: "var(--surface)",
                  border: "0.75px solid rgba(var(--divider-rgb), 0.5)",
                  display: "flex",
                  alignItems: "center",
                  gap: 11,
                  cursor: "pointer",
                  textAlign: "left",
                  color: "var(--text-primary)",
                }}
              >
                <div
                  style={{
                    width: 34,
                    height: 34,
                    borderRadius: "50%",
                    background: "rgba(var(--accent-rgb), 0.12)",
                    color: "var(--accent)",
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                    flexShrink: 0,
                  }}
                >
                  <ScenarioIconGlyph
                    iconOverride={activeScenario.icon}
                    name={activeScenario.name}
                    matchSSIDs={activeScenario.match_ssids}
                    size={17}
                  />
                </div>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: 13, fontWeight: 600 }}>{activeScenario.name}</div>
                  <div
                    style={{
                      fontSize: 11.5,
                      color: "var(--text-secondary)",
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {snap.auto_reason ||
                      describeScenario(
                        activeScenario,
                        snap.editor.all_lines,
                        snap.editor.all_rule_sets,
                        lang
                      )}
                  </div>
                </div>
                {showsScenarios ? (
                  <ChevronUp size={15} color="var(--text-secondary)" />
                ) : (
                  <ChevronDown size={15} color="var(--text-secondary)" />
                )}
              </button>
            )}

            {/* Confirmation prompt when switching scenario while connected */}
            {pendingSwitch && (
              <div
                style={{
                  padding: "9px 11px",
                  borderRadius: 10,
                  background: "rgba(var(--accent-rgb), 0.1)",
                  border: "0.75px solid rgba(var(--accent-rgb), 0.35)",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  gap: 8,
                }}
              >
                <span style={{ fontSize: 12, fontWeight: 500 }}>
                  {en
                    ? `Switch to “${pendingSwitch.name}”?`
                    : `切换到「${pendingSwitch.name}」？`}
                </span>
                <div style={{ display: "flex", gap: 6 }}>
                  <button
                    className="xd-capsule-btn"
                    style={{ height: 24, padding: "0 9px" }}
                    onClick={() => setPendingSwitch(null)}
                  >
                    {en ? "Cancel" : "取消"}
                  </button>
                  <button
                    className="xd-capsule-btn primary"
                    style={{ height: 24, padding: "0 10px" }}
                    onClick={() => {
                      const targetId = pendingSwitch.id;
                      setPendingSwitch(null);
                      void runAction(() => rpc("selectScenario", { id: targetId }));
                    }}
                  >
                    {en ? "Switch" : "切换"}
                  </button>
                </div>
              </div>
            )}

            {(!snap.preferences.automatic_scenario || showsScenarios) && (
              <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
                {snap.editor.scenarios.map((sc) => {
                  const isActive = sc.id === snap.editor.active_scenario_id;
                  const summary = describeScenario(
                    sc,
                    snap.editor.all_lines,
                    snap.editor.all_rule_sets,
                    lang
                  );
                  return (
                    <button
                      key={sc.id}
                      onClick={() => handleScenarioClick(sc)}
                      style={{
                        padding: 9,
                        borderRadius: 10,
                        background: "var(--surface)",
                        border: isActive
                          ? "1.5px solid var(--accent)"
                          : "1px solid rgba(var(--divider-rgb), 0.38)",
                        boxShadow: "var(--card-shadow)",
                        display: "flex",
                        alignItems: "center",
                        gap: 12,
                        cursor: "pointer",
                        textAlign: "left",
                        color: "var(--text-primary)",
                      }}
                    >
                      <div
                        style={{
                          width: 32,
                          height: 32,
                          borderRadius: 10,
                          background: isActive
                            ? "var(--accent)"
                            : "rgba(var(--accent-rgb), 0.12)",
                          color: isActive ? "var(--surface)" : "var(--accent)",
                          display: "flex",
                          alignItems: "center",
                          justifyContent: "center",
                          flexShrink: 0,
                        }}
                      >
                        <ScenarioIconGlyph
                          iconOverride={sc.icon}
                          name={sc.name}
                          matchSSIDs={sc.match_ssids}
                          size={17}
                        />
                      </div>
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <div style={{ fontSize: 13, fontWeight: 600 }}>{sc.name}</div>
                        <div
                          style={{
                            fontSize: 11.5,
                            color: "var(--text-secondary)",
                            overflow: "hidden",
                            textOverflow: "ellipsis",
                            whiteSpace: "nowrap",
                          }}
                        >
                          {summary}
                        </div>
                      </div>
                      {isActive ? (
                        <CheckCircle2 size={16} color="var(--accent)" />
                      ) : (
                        <Circle size={16} color="var(--divider)" />
                      )}
                    </button>
                  );
                })}
              </div>
            )}
          </div>

          {/* Account Section (mirrors iOS HomeView accountSection) */}
          {(activeVpnLine || snap.editor.all_lines.length <= 1) && (
            <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
              <span style={{ fontSize: 12.5, fontWeight: 600, padding: "0 4px" }}>
                {en ? "Account" : "账号"}
              </span>
              <button
                onClick={() => setShowsSignIn(true)}
                style={{
                  padding: "10px 13px",
                  borderRadius: 13,
                  background: "var(--surface)",
                  border: "0.75px solid rgba(var(--divider-rgb), 0.5)",
                  display: "flex",
                  alignItems: "center",
                  gap: 10,
                  cursor: "pointer",
                  textAlign: "left",
                  color: "var(--text-primary)",
                }}
              >
                <UserCircle2 size={22} color="var(--accent)" />
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: 12.5, fontWeight: 600 }}>
                    {activeVpnLine?.name || (en ? "Company VPN" : "公司 VPN")}
                  </div>
                  <div style={{ fontSize: 11.5, color: "var(--text-secondary)" }}>
                    {activeVpnLine?.vpn_username
                      ? activeVpnLine.vpn_username
                      : en
                        ? "Not signed in · Click to sign in"
                        : "未登录 · 点击登录公司账号"}
                  </div>
                </div>
                <ChevronRight size={14} color="var(--text-secondary)" />
              </button>
            </div>
          )}
        </div>

        {/* 3. Contextual Warning Rails */}
        {snap.is_proxy_only && (
          <>
            <div className="xd-divider" />
            <div
              className="xd-rail-proxy"
              style={{
                padding: "9px 14px 9px 18px",
                display: "flex",
                alignItems: "center",
                gap: 9,
              }}
            >
              <Plug size={13} color="var(--proxy-only)" style={{ flexShrink: 0 }} />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 11.5, fontWeight: 600, color: "var(--proxy-only)" }}>
                  {en
                    ? "System traffic is not going through XDial"
                    : "系统流量没有经过 XDial"}
                </div>
                <div className="selectable" style={{ fontSize: 10.5, color: "var(--text-secondary)" }}>
                  {en
                    ? `Only apps configured with ${socksEndpoint} use scenario rules.`
                    : `仅将代理设为 ${socksEndpoint} 的程序按场景分流`}
                </div>
              </div>
              <button
                title={en ? "Copy proxy address" : "复制本机代理地址"}
                onClick={async () => {
                  await navigator.clipboard.writeText(socksEndpoint);
                  setCopiedAddr(true);
                  setTimeout(() => setCopiedAddr(false), 1500);
                }}
                style={{
                  width: 26,
                  height: 26,
                  borderRadius: "50%",
                  border: "none",
                  background: "rgba(var(--proxy-only-rgb), 0.14)",
                  color: "var(--proxy-only)",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  cursor: "pointer",
                }}
              >
                {copiedAddr ? <Check size={12} /> : <Copy size={12} />}
              </button>
            </div>
          </>
        )}

        {snap.blocker_message && (
          <>
            <div className="xd-divider" />
            <div
              className="xd-rail-warning"
              style={{
                padding: "9px 14px 9px 18px",
                display: "flex",
                alignItems: "center",
                gap: 9,
              }}
            >
              <AlertTriangle size={13} color="var(--warning)" style={{ flexShrink: 0 }} />
              <div style={{ flex: 1, fontSize: 11.5, color: "var(--warning)" }}>
                {snap.blocker_message}
              </div>
              <button
                className="xd-capsule-btn"
                style={{ height: 24, padding: "0 9px" }}
                onClick={() => void rpc("openSettings", {})}
              >
                {en ? "Fix" : "前往配置"}
              </button>
            </div>
          </>
        )}

        {/* 4. Switch Failure / Busy Progress / ConfigDirty Rail */}
        {snap.switch_failure && (
          <>
            <div className="xd-divider" />
            <div
              className="xd-rail-danger"
              style={{
                padding: "10px 14px 10px 18px",
                display: "flex",
                alignItems: "flex-start",
                gap: 9,
              }}
            >
              <AlertCircle
                size={13}
                color="var(--danger)"
                style={{ marginTop: 2, flexShrink: 0 }}
              />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 11.5, fontWeight: 600, color: "var(--danger)" }}>
                  {en
                    ? `Switch to “${snap.switch_failure.to_scenario}” failed; still using “${snap.switch_failure.from_scenario}”`
                    : `切换到「${snap.switch_failure.to_scenario}」失败，仍在使用「${snap.switch_failure.from_scenario}」`}
                </div>
                <div
                  className="selectable"
                  style={{ fontSize: 10.5, color: "var(--text-secondary)", marginTop: 2 }}
                >
                  {snap.switch_failure.message}
                </div>
              </div>
              <button
                onClick={() => void rpc("dismissSwitchFailure")}
                style={{
                  width: 24,
                  height: 24,
                  borderRadius: "50%",
                  border: "none",
                  background: "transparent",
                  color: "var(--text-secondary)",
                  cursor: "pointer",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                }}
              >
                <X size={13} />
              </button>
            </div>
          </>
        )}

        {isBusy && tasks.length > 0 && (
          <>
            <div className="xd-divider" />
            <button
              onClick={() => setShowsDetails((v) => !v)}
              style={{
                minHeight: 42,
                padding: "8px 18px",
                border: "none",
                background: "transparent",
                display: "flex",
                alignItems: "center",
                gap: 8,
                cursor: "pointer",
                color: "var(--text-primary)",
                width: "100%",
              }}
            >
              <span
                style={{
                  width: 8,
                  height: 8,
                  borderRadius: "50%",
                  background: "var(--accent)",
                }}
              />
              <span style={{ fontSize: 12, flex: 1, textAlign: "left" }}>{dialTitle}</span>
              <span
                className="tabular-mono"
                style={{ fontSize: 11, color: "var(--text-secondary)" }}
              >
                {en ? "Steps" : "连接进度"} {completedTasks}/{tasks.length}
              </span>
              {showsDetails ? (
                <ChevronDown size={13} color="var(--text-secondary)" />
              ) : (
                <ChevronRight size={13} color="var(--text-secondary)" />
              )}
            </button>

            {showsDetails && (
              <div
                style={{
                  padding: "4px 18px 14px 18px",
                  display: "flex",
                  flexWrap: "wrap",
                  gap: 6,
                }}
              >
                {tasks.map((t) => {
                  const meta = taskMeta(t.state, lang);
                  return (
                    <span
                      key={t.id}
                      title={t.detail}
                      style={{
                        display: "inline-flex",
                        alignItems: "center",
                        gap: 5,
                        height: 24,
                        padding: "0 9px",
                        borderRadius: 999,
                        fontSize: 10.5,
                        color: meta.color,
                        background: "rgba(var(--text-primary-rgb), 0.045)",
                        border: "0.75px solid rgba(var(--divider-rgb), 0.45)",
                      }}
                    >
                      <span style={{ fontWeight: 600 }}>{t.name}</span>
                      <span style={{ opacity: 0.8 }}>· {meta.label}</span>
                    </span>
                  );
                })}
              </div>
            )}
          </>
        )}

        {snap.config_dirty && isConnected && !isBusy && (
          <>
            <div className="xd-divider" />
            <button
              disabled={Boolean(snap.blocker_message)}
              onClick={() => void runAction(() => rpc("reconnect"))}
              className="xd-rail-warning"
              style={{
                minHeight: 44,
                padding: "8px 18px",
                border: "none",
                width: "100%",
                display: "flex",
                alignItems: "center",
                gap: 9,
                cursor: snap.blocker_message ? "not-allowed" : "pointer",
                textAlign: "left",
              }}
            >
              <AlertTriangle size={13} color="var(--warning)" style={{ flexShrink: 0 }} />
              <span style={{ flex: 1, fontSize: 11.5, fontWeight: 500, color: "var(--warning)" }}>
                {en
                  ? "Saved changes are not applied — click to reconnect"
                  : "修改已保存，当前连接尚未应用"}
              </span>
              <span
                style={{
                  width: 26,
                  height: 26,
                  borderRadius: "50%",
                  background: "rgba(var(--accent-rgb), 0.11)",
                  color: "var(--accent)",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                }}
              >
                <RotateCw size={12} />
              </span>
            </button>
          </>
        )}

        {/* 5. Bottom Details: Speed Capsule & Active Lines / Failed Structured Diagnostics */}
        {isConnected && (
          <>
            <div className="xd-divider" />
            <div
              style={{
                height: 50,
                padding: "0 18px",
                display: "flex",
                alignItems: "center",
                justifyContent: "space-between",
              }}
            >
              <span style={{ width: 22 }} />
              {/* Centered Fixed-Slot Speed Capsule */}
              <div
                style={{
                  height: 28,
                  padding: "0 10px",
                  borderRadius: 999,
                  background: "rgba(var(--text-primary-rgb), 0.038)",
                  border: "0.75px solid rgba(var(--divider-rgb), 0.35)",
                  display: "inline-flex",
                  alignItems: "center",
                }}
              >
                <ArrowDown size={11} color="var(--text-secondary)" />
                <span
                  className="tabular-mono"
                  style={{
                    width: 72,
                    textAlign: "right",
                    fontSize: 11,
                    color: "var(--text-secondary)",
                    marginLeft: 4,
                  }}
                >
                  {formatRate(snap.download_bps)}
                </span>
                <span
                  style={{
                    width: 1,
                    height: 13,
                    background: "rgba(var(--divider-rgb), 0.45)",
                    margin: "0 8px",
                  }}
                />
                <ArrowUp size={11} color="var(--text-secondary)" />
                <span
                  className="tabular-mono"
                  style={{
                    width: 72,
                    textAlign: "right",
                    fontSize: 11,
                    color: "var(--text-secondary)",
                    marginLeft: 4,
                  }}
                >
                  {formatRate(snap.upload_bps)}
                </span>
              </div>

              <button
                onClick={() => setShowsDetails((v) => !v)}
                title={en ? "Connection details" : "展开线路与分流详情"}
                style={{
                  width: 22,
                  height: 22,
                  border: "none",
                  background: "transparent",
                  color: "var(--text-secondary)",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  cursor: "pointer",
                }}
              >
                {showsDetails ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
              </button>
            </div>

            {showsDetails && (
              <div
                style={{
                  padding: "2px 20px 16px 20px",
                  display: "flex",
                  flexDirection: "column",
                  gap: 9,
                }}
              >
                {/* Active Lines & Exit IP */}
                {(snap.active_lines || []).map((al) => (
                  <div
                    key={al.id}
                    style={{
                      minHeight: 24,
                      display: "flex",
                      alignItems: "center",
                      gap: 8,
                      fontSize: 11.5,
                    }}
                  >
                    <span style={{ fontWeight: 600, flex: 1, minWidth: 0 }}>{al.name}</span>
                    {al.exit_ipv4 && (
                      <span
                        className="tabular-mono selectable"
                        title={en ? "Click to copy IP" : "点击复制出口 IP"}
                        onClick={() => void navigator.clipboard.writeText(al.exit_ipv4 || "")}
                        style={{
                          fontSize: 11,
                          color: "var(--text-secondary)",
                          cursor: "pointer",
                        }}
                      >
                        {al.exit_ipv4}
                      </span>
                    )}
                    <LatencyBadge
                      lineId={al.id}
                      lineType={al.type}
                      record={snap.latencies[al.id]}
                      isConnected={isConnected}
                      lang={lang}
                      onTest={(id) => void rpc("testBatchLatency", { ids: [id] })}
                    />
                  </div>
                ))}

                {snap.route_mappings && snap.route_mappings.length > 0 && (
                  <>
                    <div className="xd-divider" style={{ opacity: 0.6 }} />
                    <button
                      onClick={() => setShowsPolicyMappings((v) => !v)}
                      style={{
                        border: "none",
                        background: "transparent",
                        display: "flex",
                        alignItems: "center",
                        gap: 5,
                        color: "var(--text-secondary)",
                        fontSize: 11,
                        cursor: "pointer",
                        padding: 0,
                      }}
                    >
                      {showsPolicyMappings ? (
                        <ChevronDown size={11} />
                      ) : (
                        <ChevronRight size={11} />
                      )}
                      <span>{en ? "Routing rules" : "分流规则"}</span>
                    </button>

                    {showsPolicyMappings && (
                      <div style={{ display: "flex", flexDirection: "column", gap: 5 }}>
                        {snap.route_mappings.map((rm, idx) => (
                          <div
                            key={`${rm.rule_name}-${idx}`}
                            style={{
                              display: "flex",
                              alignItems: "center",
                              gap: 6,
                              fontSize: 11,
                            }}
                          >
                            <span style={{ flex: 1, color: "var(--text-primary)" }}>
                              {rm.rule_name}
                            </span>
                            <ArrowRight size={10} color="var(--text-secondary)" />
                            <span
                              style={{
                                width: 88,
                                fontWeight: 600,
                                color: connectedColor,
                                overflow: "hidden",
                                textOverflow: "ellipsis",
                                whiteSpace: "nowrap",
                              }}
                            >
                              {rm.line_name}
                            </span>
                          </div>
                        ))}
                      </div>
                    )}
                  </>
                )}
              </div>
            )}
          </>
        )}

        {/* Structured Failure Facts (DESIGN.md §4.4) */}
        {isFailed && snap.failure && (
          <>
            <div className="xd-divider" />
            <div style={{ display: "flex", flexDirection: "column" }}>
              <div
                style={{
                  padding: "13px 20px",
                  display: "flex",
                  alignItems: "flex-start",
                  gap: 9,
                }}
              >
                <span
                  style={{
                    width: 8,
                    height: 8,
                    borderRadius: "50%",
                    background: "var(--danger)",
                    marginTop: 5,
                    flexShrink: 0,
                  }}
                />
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: 12.5, fontWeight: 600 }}>
                    {snap.failure.title}
                  </div>
                    <div
                      className="selectable"
                      style={{
                        fontSize: 11,
                        color: "var(--text-secondary)",
                        marginTop: 3,
                        wordBreak: "break-word",
                      }}
                    >
                      {snap.failure.raw_error}
                    </div>
                    {activeVpnLine && (
                      <div style={{ marginTop: 8 }}>
                        <button
                          className="xd-capsule-btn"
                          onClick={() => setShowsSignIn(true)}
                        >
                          <UserCircle2 size={12} />
                          <span>{en ? "Edit VPN Account…" : "修改 VPN 登录账号…"}</span>
                        </button>
                      </div>
                    )}
                  </div>
                </div>
              <div className="xd-divider" style={{ marginLeft: 20 }} />
              <div
                style={{
                  padding: "9px 20px",
                  fontSize: 11.5,
                  color: "var(--text-secondary)",
                  display: "flex",
                  alignItems: "center",
                  gap: 8,
                }}
              >
                <CheckCircle2
                  size={13}
                  color={snap.failure.rollback_ok ? "var(--success)" : "var(--danger)"}
                />
                <span>
                  {snap.failure.ingress_fact} · {snap.failure.rollback_fact}
                </span>
              </div>
            </div>
          </>
        )}
      </div>

      {showsSignIn && (
        <SignInSheet
          vpnLine={activeVpnLine}
          freshUnconfigured={snap.fresh_unconfigured}
          isLocked={snap.phase === "disconnecting"}
          lang={lang}
          onClose={(dismissOnboarding) => {
            setShowsSignIn(false);
            if (dismissOnboarding && snap.fresh_unconfigured) {
              void rpc("dismissOnboarding");
            }
          }}
        />
      )}
    </div>
  );
};

const MenuActionBtn: React.FC<{
  label: string;
  danger?: boolean;
  onClick: () => void;
}> = ({ label, danger, onClick }) => (
  <button
    onClick={onClick}
    style={{
      border: "none",
      background: "transparent",
      padding: "6px 10px",
      borderRadius: 6,
      textAlign: "left",
      fontSize: 12,
      color: danger ? "var(--danger)" : "var(--text-primary)",
      cursor: "pointer",
    }}
    onMouseEnter={(e) => {
      e.currentTarget.style.background = "rgba(var(--text-primary-rgb), 0.06)";
    }}
    onMouseLeave={(e) => {
      e.currentTarget.style.background = "transparent";
    }}
  >
    {label}
  </button>
);
