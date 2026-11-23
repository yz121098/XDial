import React from "react";
import { RefreshCw } from "lucide-react";
import type { LatencyRecord, LineType } from "../types";

export const LatencyBadge: React.FC<{
  lineId: string;
  lineType?: LineType | string;
  record?: LatencyRecord;
  isConnected?: boolean;
  readOnly?: boolean;
  lang?: "zh-CN" | "en";
  onTest?: (lineId: string) => void;
}> = ({
  lineId,
  lineType,
  record,
  isConnected = false,
  readOnly = false,
  lang = "zh-CN",
  onTest,
}) => {
  const needsConnection =
    !isConnected && (lineType === "vpn" || lineType === "tailscale" || lineType === "chain");

  const testing = Boolean(record?.testing);
  const ms = record?.milliseconds;
  const err = record?.error;

  let text = lang === "en" ? "Untested" : "未测速";
  let color = "var(--text-secondary)";

  if (testing) {
    text = lang === "en" ? "Testing…" : "测速中";
    color = "var(--accent)";
  } else if (ms !== undefined && ms !== null) {
    text = `${ms} ms`;
    color = ms < 260 ? "var(--success)" : ms < 600 ? "var(--warning)" : "var(--danger)";
  } else if (err) {
    text = lang === "en" ? "Failed" : "失败";
    color = "var(--danger)";
  } else if (needsConnection) {
    text = lang === "en" ? "Connect req." : "需连接";
    color = "var(--text-secondary)";
  }

  return (
    <div
      onClick={(e) => e.stopPropagation()}
      title={err || undefined}
      style={{
        display: "inline-flex",
        alignItems: "center",
        gap: 5,
        fontSize: 11,
        color,
        whiteSpace: "nowrap",
      }}
    >
      <span className="tabular-mono">{text}</span>
      {!readOnly && onTest && !needsConnection && (
        <button
          title={lang === "en" ? "Test latency" : "测速"}
          disabled={testing}
          onClick={(e) => {
            e.stopPropagation();
            onTest(lineId);
          }}
          style={{
            border: "none",
            background: "transparent",
            color: "var(--text-secondary)",
            cursor: testing ? "default" : "pointer",
            display: "inline-flex",
            alignItems: "center",
            padding: 2,
            borderRadius: 4,
          }}
        >
          <RefreshCw size={11} className={testing ? "xd-spin" : undefined} />
        </button>
      )}
    </div>
  );
};
