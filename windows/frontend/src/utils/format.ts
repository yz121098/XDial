import type { Line, RuleSet, Scenario } from "../types";

export function formatRate(bps?: number | null): string {
  if (bps === undefined || bps === null || !Number.isFinite(bps) || bps < 0) {
    return "—";
  }
  const units = ["B/s", "KB/s", "MB/s", "GB/s", "TB/s"];
  let val = bps;
  let u = 0;
  while (val >= 1000 && u < units.length - 1) {
    val /= 1000;
    u++;
  }
  return `${val.toFixed(1)} ${units[u]}`;
}

export function formatDuration(connectedAtMs?: number | null, nowMs?: number): string {
  if (!connectedAtMs) return "00:00:00";
  const diffSec = Math.max(0, Math.floor(((nowMs ?? Date.now()) - connectedAtMs) / 1000));
  const h = Math.floor(diffSec / 3600);
  const m = Math.floor((diffSec % 3600) / 60);
  const s = diffSec % 60;
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(h)}:${pad(m)}:${pad(s)}`;
}

export function lineTypeBadge(type: string, lang: "zh-CN" | "en" = "zh-CN"): string {
  const zh: Record<string, string> = {
    direct: "直连",
    vpn: "AnyConnect",
    tailscale: "Tailscale",
    trojan: "Trojan",
    shadowsocks: "Shadowsocks",
    vmess: "VMess",
    vless: "VLESS",
    anytls: "AnyTLS",
    selector: "手动选线",
    urltest: "自动测速",
    chain: "链式代理",
  };
  const en: Record<string, string> = {
    direct: "Direct",
    vpn: "AnyConnect",
    tailscale: "Tailscale",
    trojan: "Trojan",
    shadowsocks: "Shadowsocks",
    vmess: "VMess",
    vless: "VLESS",
    anytls: "AnyTLS",
    selector: "Selector",
    urltest: "Auto URLTest",
    chain: "Proxy Chain",
  };
  return (lang === "en" ? en[type] : zh[type]) || type;
}

// Mirrors apple/Home/ScenarioDescriber.swift human-readable routing summary
export function describeScenario(
  scenario: Scenario,
  allLines: Line[],
  allRules: RuleSet[],
  lang: "zh-CN" | "en" = "zh-CN"
): string {
  const lineName = (id?: string) => {
    if (!id || id === "direct") return lang === "en" ? "Direct" : "直连";
    const found = allLines.find((l) => l.id === id);
    return found ? found.name : id;
  };
  const ruleName = (id: string) => {
    const found = allRules.find((r) => r.id === id);
    return found ? found.name : id;
  };

  const defName = lineName(scenario.default_line_id);
  if (!scenario.bindings || scenario.bindings.length === 0) {
    return lang === "en"
      ? `All traffic via ${defName}`
      : scenario.default_line_id === "direct" || !scenario.default_line_id
        ? "全部流量直连"
        : `全部流量走 ${defName}`;
  }

  // Group active (enabled) rules by target line preserving first appearance order
  const groups: { lineId: string; rules: string[] }[] = [];
  for (const b of scenario.bindings) {
    const ruleObj = allRules.find((r) => r.id === b.rule_set_id);
    if (ruleObj && !ruleObj.enabled) {
      continue;
    }
    const lid = b.line_id || "direct";
    const rName = ruleObj ? ruleObj.name : ruleName(b.rule_set_id);
    const existing = groups.find((g) => g.lineId === lid);
    if (existing) {
      if (!existing.rules.includes(rName)) {
        existing.rules.push(rName);
      }
    } else {
      groups.push({ lineId: lid, rules: [rName] });
    }
  }

  if (groups.length === 0) {
    return lang === "en"
      ? `All traffic via ${defName}`
      : scenario.default_line_id === "direct" || !scenario.default_line_id
        ? "全部流量直连"
        : `全部流量走 ${defName}`;
  }

  const parts = groups.slice(0, 2).map((g) => {
    const target = lineName(g.lineId);
    const shown = g.rules.slice(0, 3).join("、");
    const suffix =
      g.rules.length > 3
        ? lang === "en"
          ? ` +${g.rules.length - 3}`
          : ` 等 ${g.rules.length} 项`
        : "";
    if (lang === "en") {
      return `${shown}${suffix} → ${target}`;
    }
    return g.lineId === "direct"
      ? `${shown}${suffix}直连`
      : `${shown}${suffix}走 ${target}`;
  });

  if (lang === "en") {
    return `${parts.join("; ")}, others → ${defName}`;
  }
  const rest =
    scenario.default_line_id === "direct" || !scenario.default_line_id
      ? "其余直连"
      : `其余走 ${defName}`;
  return `${parts.join("，")}，${rest}`;
}
