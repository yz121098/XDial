import React, { useState } from "react";
import { CheckCircle2, AlertTriangle, ShieldCheck, Copy, Check } from "lucide-react";
import { rpc } from "../../api/bridge";
import type { AppSnapshot, IngressMode, Preferences } from "../../types";

export type GeneralSubSection = "startup" | "appearance" | "system" | "about";

export const GeneralTab: React.FC<{
  snap: AppSnapshot;
  section: GeneralSubSection;
  onError: (msg: string | null) => void;
}> = ({ snap, section, onError }) => {
  const prefs = snap.preferences;
  const lang = prefs.language || "zh-CN";
  const en = lang === "en";

  const [portDraft, setPortDraft] = useState(String(prefs.socks_port || 5181));
  const [copiedDiag, setCopiedDiag] = useState(false);

  const updatePref = async (patch: Partial<Preferences>) => {
    onError(null);
    try {
      await rpc("savePreferences", { ...prefs, ...patch });
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e));
    }
  };

  if (section === "startup") {
    return (
      <div style={{ padding: "8px 18px 20px", display: "flex", flexDirection: "column", gap: 14 }}>
        <SettingGroup title={en ? "Connection & Ingress Mode" : "连接方式与本机端口"}>
          <SettingRow
            label={en ? "Connection Mode" : "连接方式"}
            desc={
              prefs.ingress_mode === "local-socks"
                ? en
                  ? "XDial runs on 127.0.0.1 only; system traffic bypasses XDial."
                  : "仅开放本机混合代理端口，系统流量不经过 XDial（暮紫状态）。"
                : prefs.ingress_mode === "wintun"
                  ? en
                    ? "Captures system TCP/UDP/DNS via Wintun virtual adapter (requires Administrator + wintun.dll)."
                    : "通过 Wintun 虚拟网卡接管全机 TCP/UDP/DNS（需管理员权限与 wintun.dll）。"
                  : en
                    ? "Configures Windows WinINET System Proxy to route browser & app traffic."
                    : "自动配置 Windows 系统代理（WinINET）并在退出或异常时自动恢复原设置。"
            }
          >
            <div className="xd-segmented">
              {(
                [
                  ["system-proxy", en ? "System Proxy" : "接管系统·代理"],
                  ["wintun", en ? "System TUN" : "接管系统·TUN"],
                  ["local-socks", en ? "SOCKS5 Only" : "仅 SOCKS5"],
                ] as [IngressMode, string][]
              ).map(([m, label]) => (
                <button
                  key={m}
                  data-active={String(prefs.ingress_mode === m)}
                  onClick={() => void updatePref({ ingress_mode: m })}
                >
                  {label}
                </button>
              ))}
            </div>
          </SettingRow>

          {prefs.ingress_mode === "wintun" && (
            <SettingRow
              label={en ? "Also Expose Local SOCKS5/HTTP" : "同时开放本机 SOCKS5/HTTP 端口"}
            >
              <SwitchBtn
                checked={prefs.also_expose_local_socks}
                onChange={(v) => void updatePref({ also_expose_local_socks: v })}
              />
            </SettingRow>
          )}

          <SettingRow
            label={en ? "Local Proxy Port (127.0.0.1)" : "本机代理监听端口（127.0.0.1）"}
            desc={en ? "Valid range: 1024–65535" : "有效范围 1024–65535，默认 5181"}
          >
            <input
              className="xd-input tabular-mono"
              style={{ width: 84, textAlign: "right" }}
              type="number"
              value={portDraft}
              onChange={(e) => setPortDraft(e.target.value)}
              onBlur={() => {
                const n = Number(portDraft);
                if (n >= 1024 && n <= 65535 && n !== prefs.socks_port) {
                  void updatePref({ socks_port: n });
                } else {
                  setPortDraft(String(prefs.socks_port));
                }
              }}
            />
          </SettingRow>
        </SettingGroup>

        <SettingGroup title={en ? "Startup & Notifications" : "启动行为与通知"}>
          <SettingRow label={en ? "Launch XDial at Login" : "登录 Windows 时自动启动"}>
            <SwitchBtn
              checked={prefs.launch_at_login}
              onChange={(v) => void updatePref({ launch_at_login: v })}
            />
          </SettingRow>

          <SettingRow label={en ? "Connect Automatically on Launch" : "启动 XDial 时自动连接"}>
            <SwitchBtn
              checked={prefs.connect_on_launch}
              onChange={(v) => void updatePref({ connect_on_launch: v })}
            />
          </SettingRow>

          <SettingRow label={en ? "Notify on Unexpected Disconnection" : "意外断线时通知"}>
            <SwitchBtn
              checked={prefs.notify_disconnect}
              onChange={(v) => void updatePref({ notify_disconnect: v })}
            />
          </SettingRow>

          <SettingRow label={en ? "Notify on Scenario Switch" : "场景切换时通知"}>
            <SwitchBtn
              checked={prefs.notify_scenario_switch}
              onChange={(v) => void updatePref({ notify_scenario_switch: v })}
            />
          </SettingRow>
        </SettingGroup>
      </div>
    );
  }

  if (section === "appearance") {
    return (
      <div style={{ padding: "8px 18px 20px", display: "flex", flexDirection: "column", gap: 14 }}>
        <SettingGroup title={en ? "Theme & Language" : "外观与语言"}>
          <SettingRow label={en ? "Appearance" : "外观主题"}>
            <div className="xd-segmented" style={{ width: 210 }}>
              {(
                [
                  ["system", en ? "System" : "跟随系统"],
                  ["light", en ? "Light" : "浅色"],
                  ["dark", en ? "Dark" : "深色"],
                ] as ["system" | "light" | "dark", string][]
              ).map(([val, label]) => (
                <button
                  key={val}
                  data-active={String(prefs.appearance === val)}
                  onClick={() => void updatePref({ appearance: val })}
                >
                  {label}
                </button>
              ))}
            </div>
          </SettingRow>

          <SettingRow label={en ? "Language" : "界面语言"}>
            <select
              className="xd-select"
              style={{ width: 150 }}
              value={prefs.language}
              onChange={(e) =>
                void updatePref({ language: e.target.value as "zh-CN" | "en" })
              }
            >
              <option value="zh-CN">简体中文</option>
              <option value="en">English</option>
            </select>
          </SettingRow>

          <SettingRow
            label={en ? "Show Live Speed in Tray Tooltip" : "在托盘悬停提示中显示实时网速"}
          >
            <SwitchBtn
              checked={prefs.show_tray_speed}
              onChange={(v) => void updatePref({ show_tray_speed: v })}
            />
          </SettingRow>
        </SettingGroup>
      </div>
    );
  }

  if (section === "system") {
    const facts = snap.system_facts;
    return (
      <div style={{ padding: "8px 18px 20px", display: "flex", flexDirection: "column", gap: 14 }}>
        <SettingGroup title={en ? "Windows Network & Driver Readiness" : "Windows 网络事实与权限检查"}>
          <SettingRow
            label={en ? "Default Underlay Interface" : "当前物理默认路由接口（Underlay）"}
            desc={
              facts.default_interface_name
                ? `${facts.default_interface_name} (${facts.default_ipv4 || "—"}) · DNS: ${(facts.dns_servers || []).join(", ")}`
                : en
                  ? "No active default interface detected"
                  : "暂未检测到活动默认网卡"
            }
          >
            <StatusPill
              ok={Boolean(facts.default_interface_name)}
              label={facts.default_interface_name ? (en ? "Ready" : "已捕获") : "—"}
            />
          </SettingRow>

          <SettingRow
            label={en ? "Wi-Fi SSID Detection" : "当前 Wi-Fi 名称读取"}
            desc={
              facts.connected_ssid
                ? `${en ? "Connected to" : "当前连接"}: ${facts.connected_ssid}`
                : en
                  ? "Ethernet or Wi-Fi disconnected / location permission off"
                  : "当前使用有线网络，或系统定位权限未允许读取 SSID"
            }
          >
            <StatusPill
              ok={Boolean(facts.connected_ssid)}
              label={facts.connected_ssid || (en ? "None" : "未连接 Wi-Fi")}
            />
          </SettingRow>

          <SettingRow
            label={en ? "Administrator Privileges (for Wintun)" : "管理员权限（仅 TUN 模式需要）"}
            desc={
              en
                ? "System Proxy and SOCKS5-only modes work without elevation."
                : "「系统代理」与「仅 SOCKS5」模式无需管理员权限即可完整运行。"
            }
          >
            <StatusPill
              ok={facts.is_elevated}
              label={facts.is_elevated ? (en ? "Elevated" : "已提权") : en ? "Standard User" : "普通用户"}
            />
          </SettingRow>

          <SettingRow
            label="wintun.dll"
            desc={
              en
                ? "Place wintun.dll next to XDial.exe to enable full-system TUN mode."
                : "将 wintun.dll 放置在程序同级目录即可启用 Wintun 虚拟网卡模式（已预留 WFP 透明拦截接口）。"
            }
          >
            <StatusPill
              ok={facts.has_wintun_dll}
              label={facts.has_wintun_dll ? (en ? "Detected" : "已就绪") : en ? "Not Found" : "未放置"}
            />
          </SettingRow>
        </SettingGroup>
      </div>
    );
  }

  return (
    <div style={{ padding: "8px 18px 20px", display: "flex", flexDirection: "column", gap: 14 }}>
      <SettingGroup title={en ? "About XDial for Windows" : "关于 XDial Windows"}>
        <div style={{ display: "flex", alignItems: "center", gap: 12, padding: "4px 0" }}>
          <ShieldCheck size={28} color="var(--accent)" />
          <div>
            <div style={{ fontSize: 17, fontWeight: 700 }}>XDial</div>
            <div className="tabular-mono selectable" style={{ fontSize: 12, color: "var(--text-secondary)" }}>
              {snap.version} · Go Control Plane + sing-box + Wails v3
            </div>
          </div>
        </div>

        <div style={{ fontSize: 12, color: "var(--text-secondary)", lineHeight: 1.55 }}>
          {en
            ? "XDial unifies Direct, Cisco AnyConnect (sslcon), Tailscale, and sing-box proxy lines under scene-based DNS & egress rules. Client contains zero telemetry; credentials are encrypted locally via Windows DPAPI."
            : "XDial 将直连、企业 AnyConnect、盒内 Tailscale 与各类代理协议统一收拢在「场景」之下裁决 DNS 与连接出口。客户端无任何遥测上报，本地配置与凭据通过 Windows DPAPI 绑定当前系统账户加密保存。"}
        </div>

        <div style={{ display: "flex", gap: 8, marginTop: 4 }}>
          <button
            className="xd-capsule-btn"
            onClick={async () => {
              const diag = await rpc<string>("exportDiagnostics");
              await navigator.clipboard.writeText(diag);
              setCopiedDiag(true);
              setTimeout(() => setCopiedDiag(false), 1800);
            }}
          >
            {copiedDiag ? <Check size={12} /> : <Copy size={12} />}
            <span>
              {copiedDiag
                ? en
                  ? "Copied Diagnostics"
                  : "已复制诊断 JSON"
                : en
                  ? "Copy Diagnostics Report"
                  : "复制运行诊断报告"}
            </span>
          </button>
        </div>
      </SettingGroup>
    </div>
  );
};

const SettingGroup: React.FC<{ title: string; children: React.ReactNode }> = ({
  title,
  children,
}) => (
  <div style={{ display: "flex", flexDirection: "column", gap: 7 }}>
    <div style={{ fontSize: 11.5, fontWeight: 600, color: "var(--text-secondary)", padding: "0 2px" }}>
      {title}
    </div>
    <div
      className="xd-card"
      style={{ padding: "6px 14px", display: "flex", flexDirection: "column" }}
    >
      {children}
    </div>
  </div>
);

const SettingRow: React.FC<{
  label: string;
  desc?: string;
  children: React.ReactNode;
}> = ({ label, desc, children }) => (
  <div
    style={{
      minHeight: 42,
      padding: "8px 0",
      display: "flex",
      alignItems: "center",
      justifyContent: "space-between",
      gap: 14,
      borderBottom: "0.75px solid rgba(var(--divider-rgb), 0.22)",
    }}
  >
    <div style={{ flex: 1, minWidth: 0 }}>
      <div style={{ fontSize: 13, fontWeight: 500 }}>{label}</div>
      {desc && (
        <div style={{ fontSize: 11, color: "var(--text-secondary)", marginTop: 2 }}>
          {desc}
        </div>
      )}
    </div>
    <div style={{ flexShrink: 0 }}>{children}</div>
  </div>
);

const SwitchBtn: React.FC<{ checked: boolean; onChange: (v: boolean) => void }> = ({
  checked,
  onChange,
}) => (
  <button
    type="button"
    className="xd-switch"
    data-checked={String(checked)}
    onClick={() => onChange(!checked)}
  >
    <span className="xd-switch-thumb" />
  </button>
);

const StatusPill: React.FC<{ ok: boolean; label: string }> = ({ ok, label }) => (
  <span
    style={{
      display: "inline-flex",
      alignItems: "center",
      gap: 5,
      height: 24,
      padding: "0 9px",
      borderRadius: 999,
      fontSize: 11,
      fontWeight: 500,
      color: ok ? "var(--success)" : "var(--warning)",
      background: ok ? "rgba(var(--success-rgb), 0.12)" : "rgba(var(--warning-rgb), 0.12)",
    }}
  >
    {ok ? <CheckCircle2 size={12} /> : <AlertTriangle size={12} />}
    <span>{label}</span>
  </span>
);
