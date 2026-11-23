import React, { useState } from "react";
import {
  Home,
  Users,
  Building2,
  Briefcase,
  BedDouble,
  Plane,
  Car,
  Coffee,
  Wifi,
  Radio,
  GraduationCap,
  Gamepad2,
  Clapperboard,
  Music,
  Terminal,
  HardDrive,
  Cloud,
  Hand,
  Globe2,
  Globe,
  ArrowLeftRight,
  ShieldCheck,
  LayoutGrid,
  Sparkles,
  Check,
} from "lucide-react";

export interface ScenarioIconDef {
  id: string;
  zh: string;
  en: string;
  icon: React.ComponentType<{ size?: number; strokeWidth?: number }>;
  nameKeywords: string[];
  ssidKeywords: string[];
  priority: number;
}

export const SCENARIO_ICONS: ScenarioIconDef[] = [
  {
    id: "home",
    zh: "家庭",
    en: "Home",
    icon: Home,
    nameKeywords: ["家庭", "家里", "家用", "住宅", "home", "house", "residential"],
    ssidKeywords: ["home", "house", "family"],
    priority: 300,
  },
  {
    id: "family",
    zh: "家人",
    en: "Family",
    icon: Users,
    nameKeywords: ["家人", "亲子", "儿童", "family", "kids", "child", "parents"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "office",
    zh: "公司",
    en: "Office",
    icon: Building2,
    nameKeywords: ["公司", "办公", "企业", "office", "corp", "company", "enterprise"],
    ssidKeywords: ["office", "corp", "company", "enterprise"],
    priority: 300,
  },
  {
    id: "work",
    zh: "工作",
    en: "Work",
    icon: Briefcase,
    nameKeywords: ["工作", "商务", "远程办公", "work", "business", "remote work"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "hotel",
    zh: "酒店",
    en: "Hotel",
    icon: BedDouble,
    nameKeywords: ["酒店", "旅馆", "住宿", "宾馆", "全季", "华住", "hotel", "motel", "inn", "lodge"],
    ssidKeywords: ["hotel", "motel", "inn", "lodge", "全季", "华住", "汉庭", "亚朵", "hilton", "marriott", "hyatt", "airbnb"],
    priority: 300,
  },
  {
    id: "travel",
    zh: "旅行",
    en: "Travel",
    icon: Plane,
    nameKeywords: ["旅行", "出差", "机场", "飞行", "在外地", "travel", "trip", "airport", "flight"],
    ssidKeywords: ["airport", "airline", "flight"],
    priority: 300,
  },
  {
    id: "commute",
    zh: "通勤",
    en: "Commute",
    icon: Car,
    nameKeywords: ["通勤", "车载", "汽车", "commute", "car", "drive"],
    ssidKeywords: ["car", "auto"],
    priority: 300,
  },
  {
    id: "cafe",
    zh: "咖啡馆",
    en: "Café",
    icon: Coffee,
    nameKeywords: ["咖啡", "咖啡馆", "coffee", "cafe", "café"],
    ssidKeywords: ["coffee", "cafe", "café", "starbucks"],
    priority: 300,
  },
  {
    id: "public_wifi",
    zh: "公共 Wi-Fi",
    en: "Public Wi-Fi",
    icon: Wifi,
    nameKeywords: ["公共", "免费", "访客", "public", "free", "guest", "wifi"],
    ssidKeywords: ["public", "free", "guest", "wifi"],
    priority: 0,
  },
  {
    id: "hotspot",
    zh: "热点",
    en: "Hotspot",
    icon: Radio,
    nameKeywords: ["热点", "随身", "共享", "hotspot", "tether", "mobile"],
    ssidKeywords: ["hotspot", "iphone", "android", "mobile"],
    priority: 300,
  },
  {
    id: "campus",
    zh: "校园",
    en: "Campus",
    icon: GraduationCap,
    nameKeywords: ["学校", "校园", "大学", "school", "campus", "university", "college"],
    ssidKeywords: ["campus", "university", "college", "eduroam"],
    priority: 300,
  },
  {
    id: "gaming",
    zh: "游戏",
    en: "Gaming",
    icon: Gamepad2,
    nameKeywords: ["游戏", "电竞", "game", "gaming", "steam", "console"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "video",
    zh: "影音",
    en: "Video",
    icon: Clapperboard,
    nameKeywords: ["影音", "视频", "电视", "电影", "media", "video", "tv", "movie", "streaming"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "music",
    zh: "音乐",
    en: "Music",
    icon: Music,
    nameKeywords: ["音乐", "音频", "music", "audio", "radio"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "development",
    zh: "开发",
    en: "Development",
    icon: Terminal,
    nameKeywords: ["开发", "编程", "测试", "dev", "development", "code", "coding", "test"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "server",
    zh: "服务器",
    en: "Server",
    icon: HardDrive,
    nameKeywords: ["服务器", "机房", "实验室", "server", "homelab", "nas", "lab"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "cloud",
    zh: "云端",
    en: "Cloud",
    icon: Cloud,
    nameKeywords: ["云端", "cloud", "aws", "gcp", "azure"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "privacy",
    zh: "隐私",
    en: "Privacy",
    icon: Hand,
    nameKeywords: ["隐私", "匿名", "privacy", "private", "anonymous"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "regional",
    zh: "国内区域",
    en: "Local Region",
    icon: Globe2,
    nameKeywords: ["国内", "本地", "本地区", "中国", "local", "region", "china", "cn", "domestic"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "global",
    zh: "全局",
    en: "Global",
    icon: Globe,
    nameKeywords: ["全局", "全代理", "海外", "国际", "在国外", "global", "worldwide", "overseas", "international"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "direct",
    zh: "直连",
    en: "Direct",
    icon: ArrowLeftRight,
    nameKeywords: ["直连", "不代理", "日常", "direct", "bypass", "no proxy"],
    ssidKeywords: [],
    priority: 300,
  },
  {
    id: "secure",
    zh: "安全 VPN",
    en: "Secure VPN",
    icon: ShieldCheck,
    nameKeywords: ["安全", "代理", "secure", "vpn", "proxy", "tunnel"],
    ssidKeywords: [],
    priority: 100,
  },
  {
    id: "general",
    zh: "通用",
    en: "General",
    icon: LayoutGrid,
    nameKeywords: [],
    ssidKeywords: [],
    priority: -1000,
  },
];

function scoreKeyword(text: string, kw: string): number {
  const normText = text.trim().toLowerCase();
  const normKw = kw.trim().toLowerCase();
  if (!normText || !normKw) return -1;
  if (normText === normKw) return 5000 + normKw.length;
  const words = normText.split(/[^a-z0-9\u4e00-\u9fa5]+/).filter(Boolean);
  if (words.includes(normKw)) return 4000 + normKw.length;
  const isNonAscii = /[^\x00-\x7F]/.test(normKw);
  if ((isNonAscii || normKw.length >= 5 || normKw === "vpn") && normText.includes(normKw)) {
    return 2000 + normKw.length;
  }
  return -1;
}

export function resolveScenarioIconDef(
  iconOverride?: string,
  name?: string,
  matchSSIDs?: string[]
): ScenarioIconDef {
  if (iconOverride && iconOverride.trim() !== "") {
    const exact = SCENARIO_ICONS.find((i) => i.id === iconOverride);
    return exact ?? SCENARIO_ICONS[SCENARIO_ICONS.length - 1];
  }
  return autoMatchScenarioIconDef(name, matchSSIDs);
}

export function autoMatchScenarioIconDef(
  name?: string,
  matchSSIDs?: string[]
): ScenarioIconDef {
  let best: ScenarioIconDef | null = null;
  let bestScore = -99999;

  if (name) {
    for (const def of SCENARIO_ICONS) {
      for (const kw of def.nameKeywords) {
        const s = scoreKeyword(name, kw);
        if (s >= 0 && s + def.priority > bestScore) {
          bestScore = s + def.priority;
          best = def;
        }
      }
    }
  }
  if (best) return best;

  if (matchSSIDs && matchSSIDs.length > 0) {
    matchSSIDs.forEach((ssid, idx) => {
      for (const def of SCENARIO_ICONS) {
        for (const kw of def.ssidKeywords) {
          const s = scoreKeyword(ssid, kw);
          if (s >= 0 && s + def.priority - idx > bestScore) {
            bestScore = s + def.priority - idx;
            best = def;
          }
        }
      }
    });
  }

  return best ?? SCENARIO_ICONS[SCENARIO_ICONS.length - 1];
}

export const ScenarioIconGlyph: React.FC<{
  iconOverride?: string;
  name?: string;
  matchSSIDs?: string[];
  size?: number;
}> = ({ iconOverride, name, matchSSIDs, size = 18 }) => {
  const def = resolveScenarioIconDef(iconOverride, name, matchSSIDs);
  const IconComp = def.icon;
  return <IconComp size={size} strokeWidth={2} />;
};

export const ScenarioIconPickerPopover: React.FC<{
  currentOverride?: string;
  scenarioName: string;
  matchSSIDs?: string[];
  lang: "zh-CN" | "en";
  onSelect: (key: string) => void;
  onClose: () => void;
}> = ({ currentOverride, scenarioName, matchSSIDs, lang, onSelect, onClose }) => {
  const autoDef = autoMatchScenarioIconDef(scenarioName, matchSSIDs);
  const activeDef = resolveScenarioIconDef(currentOverride, scenarioName, matchSSIDs);
  const [hoverDef, setHoverDef] = useState<ScenarioIconDef | null>(null);
  const isAuto = !currentOverride;
  const AutoIcon = autoDef.icon;

  const labelDef = hoverDef ?? activeDef;

  return (
    <div
      onClick={(e) => e.stopPropagation()}
      style={{
        position: "absolute",
        top: 32,
        right: 0,
        zIndex: 60,
        width: 268,
        padding: 12,
        borderRadius: 12,
        background: "var(--elevated)",
        border: "0.75px solid rgba(var(--divider-rgb), 0.65)",
        boxShadow: "var(--popover-shadow)",
        display: "flex",
        flexDirection: "column",
        gap: 9,
      }}
    >
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          fontSize: 11.5,
          fontWeight: 500,
          color: "var(--text-secondary)",
        }}
      >
        <span>{lang === "en" ? labelDef.en : labelDef.zh}</span>
        <button
          onClick={onClose}
          style={{
            border: "none",
            background: "transparent",
            color: "var(--text-secondary)",
            cursor: "pointer",
            fontSize: 11,
          }}
        >
          ✕
        </button>
      </div>

      <button
        onClick={() => {
          onSelect("");
          onClose();
        }}
        style={{
          height: 32,
          borderRadius: 8,
          padding: "0 10px",
          display: "flex",
          alignItems: "center",
          gap: 8,
          border: isAuto
            ? "1px solid rgba(var(--selection-rgb), 0.45)"
            : "0.75px solid rgba(var(--divider-rgb), 0.45)",
          background: isAuto ? "rgba(var(--selection-rgb), 0.12)" : "var(--surface)",
          color: "var(--text-primary)",
          cursor: "pointer",
          fontSize: 12,
          fontWeight: 500,
        }}
      >
        <AutoIcon size={15} strokeWidth={2} />
        <span style={{ flex: 1, textAlign: "left" }}>
          {lang === "en" ? "Automatic" : "自动匹配"}
        </span>
        {isAuto ? (
          <Check size={13} color="var(--accent)" />
        ) : (
          <Sparkles size={13} color="var(--text-secondary)" />
        )}
      </button>

      <div className="xd-divider" />

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(6, 34px)",
          gap: 8,
          justifyContent: "center",
        }}
      >
        {SCENARIO_ICONS.map((item) => {
          const IconC = item.icon;
          const selected = currentOverride === item.id;
          return (
            <button
              key={item.id}
              title={lang === "en" ? item.en : item.zh}
              onMouseEnter={() => setHoverDef(item)}
              onMouseLeave={() => setHoverDef(null)}
              onClick={() => {
                onSelect(item.id);
                onClose();
              }}
              style={{
                position: "relative",
                width: 34,
                height: 34,
                borderRadius: 8,
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                border: selected
                  ? "1px solid rgba(var(--selection-rgb), 0.45)"
                  : "0.75px solid transparent",
                background: selected
                  ? "rgba(var(--selection-rgb), 0.14)"
                  : "transparent",
                color: "var(--text-primary)",
                cursor: "pointer",
              }}
            >
              <IconC size={16} strokeWidth={2} />
            </button>
          );
        })}
      </div>
    </div>
  );
};
