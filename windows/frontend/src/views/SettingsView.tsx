import React, { useEffect, useState } from "react";
import {
  Waypoints,
  Layers,
  ListFilter,
  LayoutGrid,
  Power,
  Palette,
  ShieldCheck,
  Info,
  ChevronDown,
  MoreHorizontal,
  AlertTriangle,
  RotateCw,
} from "lucide-react";
import { rpc } from "../api/bridge";
import type { AppSnapshot, ProfileSourceItem } from "../types";
import {
  ProfileManagerModal,
  ProfileSelectorPopover,
} from "../components/ProfileManagerModal";
import { CloudHostWizardModal } from "../components/CloudHostWizardModal";
import { LinesTab, LineGroupsTab } from "./tabs/LinesAndGroupsTab";
import { RulesTab, ScenariosTab } from "./tabs/RulesAndScenariosTab";
import { GeneralTab, type GeneralSubSection } from "./tabs/GeneralTab";

type SettingsNav =
  | "lines"
  | "groups"
  | "rules"
  | "scenarios"
  | "startup"
  | "appearance"
  | "system"
  | "about";

export const SettingsView: React.FC<{ snap: AppSnapshot }> = ({ snap }) => {
  const lang = snap.preferences.language || "zh-CN";
  const en = lang === "en";

  const [nav, setNav] = useState<SettingsNav>("lines");
  const [showsProfilePopover, setShowsProfilePopover] = useState(false);
  const [showsOverflow, setShowsOverflow] = useState(false);
  const [profileModal, setProfileModal] = useState<{
    mode: "blank" | "import" | "url" | "manage";
    target?: ProfileSourceItem;
  } | null>(null);
  const [showsCloudHost, setShowsCloudHost] = useState(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);

  useEffect(() => {
    window.__xdialNavigateSettings = (section: string) => {
      const valid: SettingsNav[] = [
        "lines",
        "groups",
        "rules",
        "scenarios",
        "startup",
        "appearance",
        "system",
        "about",
      ];
      if (valid.includes(section as SettingsNav)) {
        setNav(section as SettingsNav);
      }
    };
    return () => {
      window.__xdialNavigateSettings = undefined;
    };
  }, []);

  // DESIGN.md §5.1 Page retention rule on profile switch
  const handleSelectProfile = async (sourceId: string) => {
    setErrorMsg(null);
    try {
      await rpc("selectProfileSource", { source_id: sourceId });
      if (nav !== "lines" && nav !== "rules") {
        setNav("lines");
      }
    } catch (e) {
      setErrorMsg(e instanceof Error ? e.message : String(e));
    }
  };

  const navTitle: Record<SettingsNav, string> = {
    lines: en ? "Lines" : "线路",
    groups: en ? "Line Groups" : "线路组",
    rules: en ? "Rules" : "规则",
    scenarios: en ? "Scenarios" : "场景",
    startup: en ? "Startup & Connection" : "启动与连接",
    appearance: en ? "Appearance & Language" : "外观与语言",
    system: en ? "System & Permissions" : "系统与权限",
    about: en ? "About & Updates" : "关于与更新",
  };

  const isConfigTab =
    nav === "lines" || nav === "groups" || nav === "rules" || nav === "scenarios";

  return (
    <div
      onClick={() => {
        if (showsProfilePopover) setShowsProfilePopover(false);
        if (showsOverflow) setShowsOverflow(false);
      }}
      style={{
        width: "100%",
        height: "100%",
        display: "flex",
        background: "var(--canvas)",
        overflow: "hidden",
      }}
    >
      {/* Left Sidebar (ideal 184px) */}
      <aside
        style={{
          width: 184,
          flexShrink: 0,
          background: "rgba(var(--text-primary-rgb), 0.028)",
          borderRight: "0.75px solid rgba(var(--divider-rgb), 0.45)",
          padding: "12px 10px",
          display: "flex",
          flexDirection: "column",
          gap: 14,
        }}
      >
        {/* Profile Selector Toolbar Control (DESIGN.md §5.1) */}
        <div style={{ display: "flex", alignItems: "center", gap: 6, position: "relative" }}>
          <button
            onClick={(e) => {
              e.stopPropagation();
              setShowsProfilePopover((v) => !v);
              setShowsOverflow(false);
            }}
            style={{
              flex: 1,
              minWidth: 0,
              height: 30,
              padding: "0 9px",
              borderRadius: 8,
              border: "0.75px solid rgba(var(--divider-rgb), 0.55)",
              background: "var(--surface)",
              color: "var(--text-primary)",
              display: "flex",
              alignItems: "center",
              justifyContent: "space-between",
              gap: 5,
              fontSize: 12.5,
              fontWeight: 600,
              cursor: "pointer",
            }}
          >
            <span
              style={{
                overflow: "hidden",
                textOverflow: "ellipsis",
                whiteSpace: "nowrap",
              }}
            >
              {snap.editor.editing_source_name || (en ? "Default" : "默认配置")}
            </span>
            <ChevronDown size={12} color="var(--text-secondary)" style={{ flexShrink: 0 }} />
          </button>

          <button
            onClick={(e) => {
              e.stopPropagation();
              setShowsOverflow((v) => !v);
              setShowsProfilePopover(false);
            }}
            title={en ? "Profile Actions" : "导入与管理操作"}
            style={{
              width: 30,
              height: 30,
              borderRadius: 8,
              border: "0.75px solid rgba(var(--divider-rgb), 0.55)",
              background: "var(--surface)",
              color: "var(--text-secondary)",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              cursor: "pointer",
              flexShrink: 0,
            }}
          >
            <MoreHorizontal size={14} />
          </button>

          {showsProfilePopover && (
            <ProfileSelectorPopover
              sources={snap.editor.sources}
              editingSourceId={snap.editor.editing_source_id}
              lang={lang}
              onSelectSource={(id) => void handleSelectProfile(id)}
              onOpenModal={(mode, target) => setProfileModal({ mode, target })}
              onClose={() => setShowsProfilePopover(false)}
            />
          )}

          {showsOverflow && (
            <div
              onClick={(e) => e.stopPropagation()}
              style={{
                position: "absolute",
                top: 36,
                right: 0,
                zIndex: 70,
                minWidth: 175,
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
              <OverflowBtn
                label={en ? "Import File / Text…" : "从文件导入…"}
                onClick={() => {
                  setShowsOverflow(false);
                  setProfileModal({ mode: "import" });
                }}
              />
              <OverflowBtn
                label={en ? "Add Subscription…" : "从链接订阅…"}
                onClick={() => {
                  setShowsOverflow(false);
                  setProfileModal({ mode: "url" });
                }}
              />
              <div className="xd-divider" style={{ margin: "3px 0" }} />
              <OverflowBtn
                label={en ? "Restore Defaults…" : "恢复默认配置…"}
                disabled={snap.phase !== "disconnected"}
                danger
                onClick={async () => {
                  setShowsOverflow(false);
                  try {
                    await rpc("resetToDefaults");
                  } catch (err) {
                    setErrorMsg(err instanceof Error ? err.message : String(err));
                  }
                }}
              />
            </div>
          )}
        </div>

        {/* Configuration Section */}
        <div style={{ display: "flex", flexDirection: "column", gap: 3 }}>
          <div
            style={{
              padding: "0 8px 3px",
              fontSize: 11,
              fontWeight: 600,
              color: "var(--text-secondary)",
            }}
          >
            {en ? "Configuration" : "配置"}
          </div>
          <SidebarNavItem
            active={nav === "lines"}
            icon={<Waypoints size={15} />}
            label={navTitle.lines}
            onClick={() => setNav("lines")}
          />
          <SidebarNavItem
            active={nav === "groups"}
            icon={<Layers size={15} />}
            label={navTitle.groups}
            onClick={() => setNav("groups")}
          />
          <SidebarNavItem
            active={nav === "rules"}
            icon={<ListFilter size={15} />}
            label={navTitle.rules}
            onClick={() => setNav("rules")}
          />
          <SidebarNavItem
            active={nav === "scenarios"}
            icon={<LayoutGrid size={15} />}
            label={navTitle.scenarios}
            onClick={() => setNav("scenarios")}
          />
        </div>

        {/* General Section */}
        <div style={{ display: "flex", flexDirection: "column", gap: 3 }}>
          <div
            style={{
              padding: "0 8px 3px",
              fontSize: 11,
              fontWeight: 600,
              color: "var(--text-secondary)",
            }}
          >
            {en ? "General" : "通用"}
          </div>
          <SidebarNavItem
            active={nav === "startup"}
            icon={<Power size={15} />}
            label={navTitle.startup}
            onClick={() => setNav("startup")}
          />
          <SidebarNavItem
            active={nav === "appearance"}
            icon={<Palette size={15} />}
            label={navTitle.appearance}
            onClick={() => setNav("appearance")}
          />
          <SidebarNavItem
            active={nav === "system"}
            icon={<ShieldCheck size={15} />}
            label={navTitle.system}
            onClick={() => setNav("system")}
          />
          <SidebarNavItem
            active={nav === "about"}
            icon={<Info size={15} />}
            label={navTitle.about}
            onClick={() => setNav("about")}
          />
        </div>
      </aside>

      {/* Right Detail Pane */}
      <main
        style={{
          flex: 1,
          minWidth: 0,
          display: "flex",
          flexDirection: "column",
          height: "100%",
        }}
      >
        {/* Single Unified Page Header */}
        <div
          style={{
            padding: "13px 18px 8px 18px",
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
          }}
        >
          <h1 style={{ fontSize: 20, fontWeight: 700, letterSpacing: "-0.01em" }}>
            {navTitle[nav]}
          </h1>
          {isConfigTab && (
            <span style={{ fontSize: 11.5, color: "var(--text-secondary)" }}>
              {nav === "groups" || nav === "scenarios"
                ? en
                  ? "Global Scope"
                  : "全局共享"
                : `${en ? "Source" : "当前来源"}：${snap.editor.editing_source_name}`}
            </span>
          )}
        </div>

        {errorMsg && (
          <div
            className="xd-rail-danger selectable"
            style={{
              margin: "0 16px 6px",
              padding: "7px 12px",
              borderRadius: 8,
              border: "0.75px solid rgba(var(--danger-rgb), 0.35)",
              fontSize: 12,
              color: "var(--danger)",
              display: "flex",
              justifyContent: "space-between",
              alignItems: "center",
            }}
          >
            <span>{errorMsg}</span>
            <button
              onClick={() => setErrorMsg(null)}
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

        {/* Shared Dirty Banner (DESIGN.md §5.6) */}
        {snap.config_dirty && snap.phase === "connected" && (
          <div
            className="xd-rail-warning"
            style={{
              minHeight: 36,
              padding: "0 16px",
              borderTop: "0.75px solid rgba(var(--divider-rgb), 0.35)",
              borderBottom: "0.75px solid rgba(var(--divider-rgb), 0.35)",
              display: "flex",
              alignItems: "center",
              gap: 9,
            }}
          >
            <AlertTriangle size={13} color="var(--warning)" />
            <span style={{ flex: 1, fontSize: 12, fontWeight: 500, color: "var(--warning)" }}>
              {en ? "Saved changes are not applied to current connection" : "修改已保存，当前连接尚未应用"}
            </span>
            <button
              className="xd-capsule-btn primary"
              style={{ height: 24, padding: "0 10px" }}
              disabled={Boolean(snap.blocker_message)}
              onClick={() => void rpc("reconnect")}
            >
              <RotateCw size={11} />
              <span>{en ? "Apply & Reconnect" : "应用并重连"}</span>
            </button>
          </div>
        )}

        {/* Active Tab Content */}
        <div style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
          {nav === "lines" && (
            <LinesTab
              snap={snap}
              onOpenCloudHost={() => setShowsCloudHost(true)}
              onError={setErrorMsg}
            />
          )}
          {nav === "groups" && <LineGroupsTab snap={snap} onError={setErrorMsg} />}
          {nav === "rules" && <RulesTab snap={snap} onError={setErrorMsg} />}
          {nav === "scenarios" && <ScenariosTab snap={snap} onError={setErrorMsg} />}
          {(nav === "startup" ||
            nav === "appearance" ||
            nav === "system" ||
            nav === "about") && (
            <GeneralTab
              snap={snap}
              section={nav as GeneralSubSection}
              onError={setErrorMsg}
            />
          )}
        </div>
      </main>

      {profileModal && (
        <ProfileManagerModal
          mode={profileModal.mode}
          target={profileModal.target}
          canDelete={snap.editor.sources.length > 1}
          lang={lang}
          onClose={() => setProfileModal(null)}
        />
      )}

      {showsCloudHost && (
        <CloudHostWizardModal lang={lang} onClose={() => setShowsCloudHost(false)} />
      )}
    </div>
  );
};

const SidebarNavItem: React.FC<{
  active: boolean;
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
}> = ({ active, icon, label, onClick }) => (
  <button
    onClick={onClick}
    style={{
      height: 31,
      padding: "0 9px",
      borderRadius: 7,
      border: "none",
      background: active ? "rgba(var(--selection-rgb), 0.16)" : "transparent",
      color: active ? "var(--text-primary)" : "var(--text-secondary)",
      fontWeight: active ? 600 : 500,
      fontSize: 12.5,
      display: "flex",
      alignItems: "center",
      gap: 8,
      cursor: "pointer",
      textAlign: "left",
    }}
  >
    <span style={{ color: active ? "var(--accent)" : "var(--text-secondary)", display: "inline-flex" }}>
      {icon}
    </span>
    <span>{label}</span>
  </button>
);

const OverflowBtn: React.FC<{
  label: string;
  disabled?: boolean;
  danger?: boolean;
  onClick: () => void;
}> = ({ label, disabled, danger, onClick }) => (
  <button
    disabled={disabled}
    onClick={onClick}
    style={{
      border: "none",
      background: "transparent",
      padding: "6px 10px",
      borderRadius: 6,
      textAlign: "left",
      fontSize: 12,
      color: danger ? "var(--danger)" : "var(--text-primary)",
      opacity: disabled ? 0.45 : 1,
      cursor: disabled ? "not-allowed" : "pointer",
    }}
  >
    {label}
  </button>
);
