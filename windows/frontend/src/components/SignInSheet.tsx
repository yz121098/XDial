import React, { useEffect, useRef, useState } from "react";
import {
  User,
  KeyRound,
  Eye,
  EyeOff,
  Server,
  ShieldCheck,
  FileUp,
  AlertCircle,
  LogOut,
} from "lucide-react";
import { refreshSnapshot, rpc } from "../api/bridge";
import type { Line } from "../types";

export const SignInSheet: React.FC<{
  vpnLine?: Line;
  freshUnconfigured: boolean;
  isLocked: boolean;
  lang: "zh-CN" | "en";
  onClose: (dismissOnboarding?: boolean) => void;
}> = ({ vpnLine, freshUnconfigured, isLocked, lang, onClose }) => {
  const en = lang === "en";
  const fileRef = useRef<HTMLInputElement | null>(null);

  const [server, setServer] = useState(vpnLine?.vpn_server || "");
  const [username, setUsername] = useState(vpnLine?.vpn_username || "");
  const [password, setPassword] = useState(vpnLine?.vpn_password || "");
  const [editingServer, setEditingServer] = useState(
    !vpnLine?.vpn_server || vpnLine.vpn_server.trim() === ""
  );
  const [showPassword, setShowPassword] = useState(false);
  const [busy, setBusy] = useState(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);
  const [importedHint, setImportedHint] = useState<string | null>(null);

  // Sync server field if user imports a mobile preset file while sheet is open
  useEffect(() => {
    if (vpnLine?.vpn_server && !server) {
      setServer(vpnLine.vpn_server);
      setEditingServer(false);
    }
    if (vpnLine?.vpn_username && !username) {
      setUsername(vpnLine.vpn_username);
    }
  }, [vpnLine?.vpn_server, vpnLine?.vpn_username]);

  const lineName =
    vpnLine?.name?.trim() || (en ? "Company VPN" : "公司 VPN");

  const canSubmit =
    !isLocked &&
    !busy &&
    server.trim().length > 0 &&
    username.trim().length > 0 &&
    password.length > 0;

  const handleSubmit = async () => {
    if (!canSubmit) return;
    setBusy(true);
    setErrorMsg(null);
    try {
      await rpc("signInAndConnect", {
        line_id: vpnLine?.id || "",
        server: server.trim(),
        username: username.trim(),
        password,
      });
      await refreshSnapshot();
      setPassword("");
      onClose(false);
    } catch (e) {
      setErrorMsg(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const handleImportMobileJSON = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    setBusy(true);
    setErrorMsg(null);
    setImportedHint(null);
    try {
      const text = await file.text();
      const baseName = file.name.replace(/\.(json|ya?ml|conf|txt)$/i, "") || "公司预置";
      await rpc("importProfileContent", { name: baseName, content: text });
      const fresh = await refreshSnapshot();
      const importedVpn = fresh?.editor.all_lines.find((l) => l.type === "vpn");
      if (importedVpn?.vpn_server) {
        setServer(importedVpn.vpn_server);
        setEditingServer(false);
      }
      setImportedHint(
        en
          ? "Scenarios & server imported! Enter your account below to connect."
          : "已成功载入全部场景与服务器地址！继续填写下方账号即可登录连接。"
      );
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  };

  const handleSignOut = async () => {
    if (!vpnLine || isLocked) return;
    setBusy(true);
    try {
      await rpc("saveLine", {
        ...vpnLine,
        vpn_username: "",
        vpn_password: "",
      });
      setUsername("");
      setPassword("");
      onClose(false);
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      style={{
        position: "fixed",
        inset: 0,
        zIndex: 90,
        background: "var(--canvas)",
        display: "flex",
        flexDirection: "column",
        border: "0.75px solid rgba(var(--divider-rgb), 0.45)",
      }}
    >
      {/* Top Toolbar */}
      <div
        style={{
          height: 46,
          padding: "0 16px",
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          flexShrink: 0,
        }}
      >
        <span style={{ fontSize: 13, fontWeight: 700, color: "var(--text-secondary)" }}>
          XDial
        </span>
        <button
          onClick={() => onClose(true)}
          style={{
            border: "none",
            background: "transparent",
            color: "var(--accent)",
            fontSize: 12.5,
            fontWeight: 500,
            cursor: "pointer",
            padding: "4px 6px",
          }}
        >
          {freshUnconfigured
            ? en
              ? "Skip for now"
              : "暂不登录"
            : en
              ? "Cancel"
              : "取消"}
        </button>
      </div>

      {/* Scrollable Sign-In Body (mirrors ios/XDialIOS/SignInSheet.swift) */}
      <div
        style={{
          flex: 1,
          overflowY: "auto",
          padding: "4px 20px 18px 20px",
          display: "flex",
          flexDirection: "column",
          gap: 15,
        }}
      >
        <div>
          <h2 style={{ fontSize: 22, fontWeight: 700, letterSpacing: "-0.015em" }}>
            {en ? `Sign in to ${lineName}` : `登录${lineName}`}
          </h2>
          <p
            style={{
              fontSize: 12.5,
              color: "var(--text-secondary)",
              marginTop: 5,
              lineHeight: 1.45,
            }}
          >
            {en
              ? "Use your company account. You only need to do this once."
              : "用你的公司账号登录，只需要填这一次。"}
          </p>
        </div>

        {/* One-Click Mobile Preset Import Helper when unseeded */}
        {(freshUnconfigured || !vpnLine?.vpn_server) && (
          <div
            style={{
              padding: "10px 12px",
              borderRadius: 11,
              background: "rgba(var(--accent-rgb), 0.08)",
              border: "0.75px solid rgba(var(--accent-rgb), 0.25)",
              display: "flex",
              alignItems: "center",
              justifyContent: "space-between",
              gap: 10,
            }}
          >
            <div style={{ minWidth: 0, flex: 1 }}>
              <div style={{ fontSize: 12, fontWeight: 600, color: "var(--text-primary)" }}>
                {en ? "Sync Scenarios from Phone" : "已有手机端配置？"}
              </div>
              <div style={{ fontSize: 11, color: "var(--text-secondary)", marginTop: 1 }}>
                {en
                  ? "Import mobile JSON to load all scenarios & server"
                  : "导入手机导出的 JSON 自动载入服务器与公司/国内场景"}
              </div>
            </div>
            <input
              ref={fileRef}
              type="file"
              accept=".json,.yaml,.yml,.conf"
              style={{ display: "none" }}
              onChange={handleImportMobileJSON}
            />
            <button
              className="xd-capsule-btn"
              disabled={busy}
              onClick={() => fileRef.current?.click()}
            >
              <FileUp size={12} />
              <span>{en ? "Import" : "导入文件"}</span>
            </button>
          </div>
        )}

        {importedHint && (
          <div style={{ fontSize: 11.5, color: "var(--success)", fontWeight: 500 }}>
            ✓ {importedHint}
          </div>
        )}

        {/* Grouped Account Credentials Card */}
        <div
          style={{
            borderRadius: 12,
            background: "var(--surface)",
            border: "0.75px solid rgba(var(--divider-rgb), 0.55)",
            padding: "0 13px",
            display: "flex",
            flexDirection: "column",
            opacity: isLocked ? 0.6 : 1,
          }}
        >
          <div style={{ height: 46, display: "flex", alignItems: "center", gap: 10 }}>
            <User size={15} color="var(--text-secondary)" style={{ flexShrink: 0 }} />
            <input
              autoFocus={Boolean(server.trim())}
              disabled={isLocked}
              placeholder={en ? "Username" : "用户名（公司账号）"}
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              style={bareInputStyle}
            />
          </div>

          <div className="xd-divider" />

          <div style={{ height: 46, display: "flex", alignItems: "center", gap: 10 }}>
            <KeyRound size={15} color="var(--text-secondary)" style={{ flexShrink: 0 }} />
            <input
              type={showPassword ? "text" : "password"}
              disabled={isLocked}
              placeholder={en ? "Password" : "密码"}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && canSubmit) {
                  void handleSubmit();
                }
              }}
              style={bareInputStyle}
            />
            <button
              type="button"
              onClick={() => setShowPassword((v) => !v)}
              title={showPassword ? (en ? "Hide password" : "隐藏密码") : en ? "Show password" : "显示密码"}
              style={{
                border: "none",
                background: "transparent",
                color: "var(--text-secondary)",
                cursor: "pointer",
                padding: 4,
                display: "inline-flex",
              }}
            >
              {showPassword ? <EyeOff size={15} /> : <Eye size={15} />}
            </button>
          </div>
        </div>

        {/* Collapsible / Editable Server Row (mirrors SignInSheet.swift serverRow) */}
        {editingServer ? (
          <div
            style={{
              height: 46,
              borderRadius: 12,
              background: "var(--surface)",
              border: "0.75px solid rgba(var(--divider-rgb), 0.55)",
              padding: "0 13px",
              display: "flex",
              alignItems: "center",
              gap: 10,
            }}
          >
            <Server size={15} color="var(--text-secondary)" style={{ flexShrink: 0 }} />
            <input
              autoFocus={!server.trim()}
              disabled={isLocked}
              className="tabular-mono"
              placeholder={en ? "Server address:port (e.g. vpn.corp.com:8443)" : "公司 VPN 服务器地址:端口"}
              value={server}
              onChange={(e) => setServer(e.target.value)}
              style={bareInputStyle}
            />
          </div>
        ) : (
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              padding: "0 4px",
              fontSize: 12,
            }}
          >
            <span style={{ color: "var(--text-secondary)" }}>
              {en ? "Server" : "服务器"}
            </span>
            <span
              className="tabular-mono"
              style={{
                flex: 1,
                overflow: "hidden",
                textOverflow: "ellipsis",
                whiteSpace: "nowrap",
              }}
            >
              {server}
            </span>
            <button
              disabled={isLocked}
              onClick={() => setEditingServer(true)}
              style={{
                border: "none",
                background: "transparent",
                color: "var(--accent)",
                fontSize: 12,
                fontWeight: 500,
                cursor: "pointer",
              }}
            >
              {en ? "Edit" : "修改"}
            </button>
          </div>
        )}

        {errorMsg && (
          <div
            className="selectable"
            style={{
              display: "flex",
              alignItems: "flex-start",
              gap: 7,
              color: "var(--danger)",
              fontSize: 12,
            }}
          >
            <AlertCircle size={14} style={{ marginTop: 2, flexShrink: 0 }} />
            <span>{errorMsg}</span>
          </div>
        )}

        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 7,
            fontSize: 11.5,
            color: "var(--text-secondary)",
          }}
        >
          <ShieldCheck size={14} style={{ flexShrink: 0 }} />
          <span>
            {en
              ? "Your account is encrypted locally via Windows DPAPI."
              : "账号和密码通过 Windows DPAPI 加密，只保存在这台电脑里。"}
          </span>
        </div>

        {!isLocked && vpnLine?.vpn_username && (
          <div>
            <button
              className="xd-capsule-btn danger"
              disabled={busy}
              onClick={() => void handleSignOut()}
            >
              <LogOut size={12} />
              <span>{en ? "Sign Out Account" : "退出当前账号"}</span>
            </button>
          </div>
        )}
      </div>

      {/* Bottom Primary Submit Bar */}
      <div
        style={{
          padding: "10px 20px 16px 20px",
          borderTop: "0.75px solid rgba(var(--divider-rgb), 0.35)",
          background: "var(--canvas)",
        }}
      >
        <button
          disabled={!canSubmit}
          onClick={() => void handleSubmit()}
          style={{
            width: "100%",
            height: 40,
            borderRadius: 11,
            border: "none",
            background: "var(--accent)",
            color: "var(--canvas)",
            fontSize: 13.5,
            fontWeight: 600,
            cursor: canSubmit ? "pointer" : "not-allowed",
            opacity: canSubmit ? 1 : 0.45,
            transition: "opacity 0.15s ease",
          }}
        >
          {busy
            ? en
              ? "Signing in…"
              : "正在登录并连接…"
            : en
              ? "Sign In and Connect"
              : "登录并连接"}
        </button>
      </div>
    </div>
  );
};

const bareInputStyle: React.CSSProperties = {
  flex: 1,
  minWidth: 0,
  border: "none",
  outline: "none",
  background: "transparent",
  color: "var(--text-primary)",
  fontSize: 13,
  fontFamily: "inherit",
};
