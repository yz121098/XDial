import React, { useState } from "react";
import type { Line } from "../types";
import { lineTypeBadge } from "../utils/format";

interface Props {
  chain: Line;
  lines: Line[];
  en: boolean;
  onCancel: () => void;
  onSave: (chain: Line) => Promise<void>;
}

export function ChainEditor({ chain, lines, en, onCancel, onSave }: Props) {
  const [name, setName] = useState(chain.name);
  const [hops, setHops] = useState(chain.group_members || ["", ""]);
  const [saving, setSaving] = useState(false);
  const proxies = lines.filter(l => ["vless", "anytls", "trojan", "shadowsocks", "vmess"].includes(l.type));
  const candidates = (i: number) => i === 0 ? [...lines.filter(l => l.type === "vpn"), ...proxies] : proxies;
  const complete = name.trim() && hops.length >= 2 && new Set(hops).size === hops.length &&
    hops.every((id, i) => candidates(i).some(l => l.id === id && l.enabled));
  return <form className="xd-card" style={{ padding: 12, display: "grid", gap: 8 }} onSubmit={async e => {
    e.preventDefault();
    if (!complete || saving) return;
    setSaving(true);
    try { await onSave({ ...chain, name: name.trim(), group_members: hops }); }
    finally { setSaving(false); }
  }}>
    <strong>{en ? "Proxy chain" : "链式代理"}</strong>
    <input className="xd-input" aria-label={en ? "Chain name" : "链路名称"} value={name} onChange={e => setName(e.target.value)} />
    {hops.map((id, i) => {
      const label = i === 0 ? (en ? "Entry" : "入口") : i === hops.length - 1 ? (en ? "Exit" : "出口") : (en ? "Relay" : "中继");
      return <div key={i} style={{ display: "flex", gap: 8, alignItems: "center" }}>
        <label htmlFor={`chain-hop-${i}`}>{i + 1}. {label}</label>
        <select id={`chain-hop-${i}`} value={id} className="xd-input" style={{ flex: 1 }}
          onChange={e => setHops(hops.map((v, j) => j === i ? e.target.value : v))}>
          <option value="">{en ? "Choose a line" : "选择线路"}</option>
          {candidates(i).map(l => <option key={l.id} value={l.id} disabled={!l.enabled || (l.id !== id && hops.includes(l.id))}>
            {l.name} · {lineTypeBadge(l.type, en ? "en" : "zh-CN")}</option>)}
        </select>
        {hops.length > 2 && <button type="button" className="xd-capsule-btn" aria-label={`${en ? "Remove hop" : "移除跳点"} ${i + 1}`}
          onClick={() => setHops(hops.filter((_, j) => j !== i))}>✕</button>}
      </div>;
    })}
    <small>{en ? "Create individual lines first. Traffic follows the order above. Select this chain as the Home scenario’s default exit or in a rule binding." :
      "先在线路页配置各跳。流量按上面的顺序连接，最后一跳是出口。保存后，在「在家」场景中把这条链设为默认出口，或仅绑定指定规则。"}</small>
    <div style={{ display: "flex", gap: 8 }}>
      <button className="xd-capsule-btn" type="button" onClick={() => setHops([...hops, ""])}>{en ? "Add hop" : "增加一跳"}</button>
      <button className="xd-capsule-btn" type="button" onClick={onCancel}>{en ? "Cancel" : "取消"}</button>
      <button className="xd-capsule-btn" type="submit" disabled={!complete || saving}>{en ? "Save chain" : "保存链路"}</button>
    </div>
  </form>;
}
