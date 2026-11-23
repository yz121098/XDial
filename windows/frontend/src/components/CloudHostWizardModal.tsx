import React, { useEffect, useState } from "react";
import { ShieldAlert, CheckCircle2, Server, X } from "lucide-react";
import { rpc } from "../api/bridge";

interface PlanStep {
  id: string;
  title: string;
  title_en: string;
  detail?: string;
}

export const CloudHostWizardModal: React.FC<{
  lang: "zh-CN" | "en";
  onClose: () => void;
}> = ({ lang, onClose }) => {
  const en = lang === "en";
  const [step, setStep] = useState<"input" | "fingerprint" | "plan" | "deploying" | "done">(
    "input"
  );
  const [lineName, setLineName] = useState("");
  const [host, setHost] = useState("");
  const [sshPort, setSshPort] = useState(22);
  const [user, setUser] = useState("root");
  const [authMode, setAuthMode] = useState<"password" | "key">("password");
  const [password, setPassword] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [sudoPassword, setSudoPassword] = useState("");

  const [fingerprint, setFingerprint] = useState("");
  const [keyType, setKeyType] = useState("");
  const [proxyPort, setProxyPort] = useState(0);
  const [planSteps, setPlanSteps] = useState<PlanStep[]>([]);
  const [events, setEvents] = useState<{ step: string; status: string; message?: string }[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    window.__xdialCloudHostEvent = (ev) => {
      setEvents((prev) => {
        const idx = prev.findIndex((p) => p.step === ev.step);
        if (idx >= 0) {
          const copy = [...prev];
          copy[idx] = ev;
          return copy;
        }
        return [...prev, ev];
      });
    };
    return () => {
      window.__xdialCloudHostEvent = undefined;
      void rpc("cloudHostClose");
    };
  }, []);

  const closeClean = () => {
    setPassword("");
    setPrivateKey("");
    setSudoPassword("");
    onClose();
  };

  const scanHostKey = async () => {
    setBusy(true);
    setErr(null);
    try {
      const res = await rpc<{ fingerprint: string; key_type: string }>("cloudHostScanKey", {
        host: host.trim(),
        port: sshPort,
      });
      setFingerprint(res.fingerprint);
      setKeyType(res.key_type);
      setStep("fingerprint");
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const inspectAndPlan = async () => {
    setBusy(true);
    setErr(null);
    try {
      const report = await rpc<{ suggested_port: number; blocking?: { message: string }[] }>(
        "cloudHostInspect",
        {
          host: host.trim(),
          port: sshPort,
          user: user.trim(),
          fingerprint,
          password: authMode === "password" ? password : "",
          private_key: authMode === "key" ? privateKey : "",
          sudo_password: sudoPassword,
        }
      );
      if (report.blocking && report.blocking.length > 0) {
        throw new Error(report.blocking[0].message);
      }
      const p = report.suggested_port || 24816;
      setProxyPort(p);
      const pl = await rpc<{ port: number; steps: PlanStep[] }>("cloudHostPlan", { port: p });
      setPlanSteps(pl.steps || []);
      setStep("plan");
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const updatePortPlan = async (newPort: number) => {
    setProxyPort(newPort);
    setPlanSteps([]);
    setBusy(true);
    setErr(null);
    try {
      const pl = await rpc<{ port: number; steps: PlanStep[] }>("cloudHostPlan", {
        port: newPort,
      });
      setPlanSteps(pl.steps || []);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const deployNow = async () => {
    setStep("deploying");
    setEvents([]);
    setBusy(true);
    setErr(null);
    try {
      await rpc("cloudHostDeployAndSave", {
        port: proxyPort,
        name: lineName.trim() || `${en ? "My Server" : "云主机"} ${host.trim()}`,
      });
      setStep("done");
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      onClick={closeClean}
      style={{
        position: "fixed",
        inset: 0,
        zIndex: 110,
        background: "rgba(0, 0, 0, 0.42)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        padding: 20,
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{
          width: 480,
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
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <Server size={16} color="var(--accent)" />
            <span style={{ fontSize: 15, fontWeight: 700 }}>
              {en ? "Add My Linux Cloud Server" : "添加我的云主机（Shadowsocks 2022）"}
            </span>
          </div>
          <button
            onClick={closeClean}
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

        {step === "input" && (
          <>
            <div style={{ fontSize: 11.5, color: "var(--text-secondary)" }}>
              {en
                ? "Connect via SSH to Ubuntu 20.04+ / Debian 12+ to automatically deploy a signed sing-box Shadowsocks 2022 service. Credentials stay in memory only."
                : "通过 SSH 在自有 Ubuntu 20.04+ / Debian 12+ 服务器上自动部署并测速验证 Shadowsocks 2022 服务。发送凭据前会先核对主机指纹，密码与私钥仅驻留本次内存。"}
            </div>

            <div style={{ display: "grid", gridTemplateColumns: "1fr 96px", gap: 8 }}>
              <input
                className="xd-input"
                placeholder={en ? "Server IP / Host" : "服务器 IP 或主机名"}
                value={host}
                onChange={(e) => setHost(e.target.value)}
              />
              <input
                className="xd-input tabular-mono"
                type="number"
                placeholder="22"
                value={sshPort}
                onChange={(e) => setSshPort(Number(e.target.value) || 22)}
              />
            </div>

            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
              <input
                className="xd-input"
                placeholder={en ? "SSH Username (e.g. root)" : "SSH 用户名（如 root / ubuntu）"}
                value={user}
                onChange={(e) => setUser(e.target.value)}
              />
              <input
                className="xd-input"
                placeholder={en ? "Line Display Name (optional)" : "保存后的线路名称（可选）"}
                value={lineName}
                onChange={(e) => setLineName(e.target.value)}
              />
            </div>

            <div className="xd-segmented" style={{ width: 200 }}>
              <button
                data-active={String(authMode === "password")}
                onClick={() => setAuthMode("password")}
              >
                {en ? "Password" : "密码登录"}
              </button>
              <button
                data-active={String(authMode === "key")}
                onClick={() => setAuthMode("key")}
              >
                {en ? "Private Key" : "SSH 私钥"}
              </button>
            </div>

            {authMode === "password" ? (
              <input
                className="xd-input"
                type="password"
                placeholder={en ? "SSH Password" : "SSH 登录密码"}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            ) : (
              <textarea
                className="xd-textarea tabular-mono"
                rows={4}
                placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
                value={privateKey}
                onChange={(e) => setPrivateKey(e.target.value)}
              />
            )}

            {user.trim() !== "root" && (
              <input
                className="xd-input"
                type="password"
                placeholder={en ? "sudo password (if required)" : "sudo 提权密码（免密 sudo 可留空）"}
                value={sudoPassword}
                onChange={(e) => setSudoPassword(e.target.value)}
              />
            )}
          </>
        )}

        {step === "fingerprint" && (
          <div
            style={{
              padding: 12,
              borderRadius: 10,
              background: "var(--surface)",
              border: "0.75px solid rgba(var(--divider-rgb), 0.5)",
              display: "flex",
              flexDirection: "column",
              gap: 8,
            }}
          >
            <div style={{ display: "flex", alignItems: "center", gap: 7, fontWeight: 600 }}>
              <ShieldAlert size={15} color="var(--warning)" />
              <span>{en ? "Verify Server Host Key Fingerprint" : "发送凭据前请核对服务器公钥指纹"}</span>
            </div>
            <div className="tabular-mono selectable" style={{ fontSize: 12, wordBreak: "break-all" }}>
              {keyType}: {fingerprint}
            </div>
            <div style={{ fontSize: 11, color: "var(--text-secondary)" }}>
              {en
                ? "Compare with `ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub` on your server."
                : "可在服务器上执行 ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub 比对一致后再继续。"}
            </div>
          </div>
        )}

        {step === "plan" && (
          <>
            <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
              <span style={{ fontSize: 12, fontWeight: 600 }}>
                {en ? "Proxy Port (TCP/UDP)" : "代理监听端口（1024–65535）"}
              </span>
              <input
                className="xd-input tabular-mono"
                style={{ width: 100 }}
                type="number"
                value={proxyPort}
                onChange={(e) => void updatePortPlan(Number(e.target.value))}
              />
            </div>

            <div
              style={{
                padding: 11,
                borderRadius: 10,
                background: "var(--surface)",
                border: "0.75px solid rgba(var(--divider-rgb), 0.5)",
                display: "flex",
                flexDirection: "column",
                gap: 6,
              }}
            >
              <div style={{ fontSize: 11.5, fontWeight: 600, color: "var(--text-secondary)" }}>
                {en ? "Planned Server Changes:" : "确认后将在服务器上执行以下变更（失败自动回滚）："}
              </div>
              {planSteps.map((s, idx) => (
                <div key={s.id} style={{ fontSize: 12 }}>
                  {idx + 1}. {en ? s.title_en : s.title}
                </div>
              ))}
            </div>
          </>
        )}

        {(step === "deploying" || step === "done") && (
          <div
            style={{
              padding: 12,
              borderRadius: 10,
              background: "var(--surface)",
              border: "0.75px solid rgba(var(--divider-rgb), 0.5)",
              display: "flex",
              flexDirection: "column",
              gap: 6,
            }}
          >
            {events.map((ev) => (
              <div
                key={ev.step}
                style={{ display: "flex", alignItems: "center", gap: 8, fontSize: 12 }}
              >
                <span style={{ fontWeight: 600 }}>{ev.step}</span>
                <span style={{ color: "var(--text-secondary)" }}>· {ev.status}</span>
                {ev.message && <span>({ev.message})</span>}
              </div>
            ))}
            {step === "done" && (
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 7,
                  color: "var(--success)",
                  fontWeight: 600,
                  marginTop: 4,
                }}
              >
                <CheckCircle2 size={15} />
                <span>
                  {en
                    ? "Deployed, verified HTTPS & saved to Lines!"
                    : "部署并通过 HTTPS 连通与测速验证，已自动保存为普通线路！"}
                </span>
              </div>
            )}
          </div>
        )}

        {err && (
          <div className="selectable" style={{ fontSize: 11.5, color: "var(--danger)" }}>
            {err}
          </div>
        )}

        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
          {step === "deploying" && busy ? (
            <button className="xd-capsule-btn danger" onClick={() => void rpc("cloudHostCancel")}>
              {en ? "Cancel & Rollback" : "取消并回滚"}
            </button>
          ) : (
            <button className="xd-capsule-btn" onClick={closeClean}>
              {step === "done" ? (en ? "Done" : "完成") : en ? "Cancel" : "关闭"}
            </button>
          )}

          {step === "input" && (
            <button
              className="xd-capsule-btn primary"
              disabled={busy || !host.trim()}
              onClick={() => void scanHostKey()}
            >
              {busy ? (en ? "Scanning…" : "正在读取指纹…") : en ? "Next: Verify Key" : "下一步：核对主机指纹"}
            </button>
          )}
          {step === "fingerprint" && (
            <button
              className="xd-capsule-btn primary"
              disabled={busy}
              onClick={() => void inspectAndPlan()}
            >
              {busy ? (en ? "Inspecting…" : "正在只读检查服务器…") : en ? "Trust & Inspect" : "指纹无误，检查服务器"}
            </button>
          )}
          {step === "plan" && (
            <button
              className="xd-capsule-btn primary"
              disabled={busy || planSteps.length === 0}
              onClick={() => void deployNow()}
            >
              {en ? "Confirm & Deploy" : "确认部署并验证"}
            </button>
          )}
        </div>
      </div>
    </div>
  );
};
