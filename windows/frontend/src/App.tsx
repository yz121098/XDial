import React, { useEffect } from "react";
import { useSnapshot } from "./api/bridge";
import { FlyoutView } from "./views/FlyoutView";
import { SettingsView } from "./views/SettingsView";

export const App: React.FC = () => {
  const snap = useSnapshot();
  const params = new URLSearchParams(window.location.search);
  const view = params.get("view") === "settings" ? "settings" : "flyout";

  // Apply adaptive light / dark / system theme onto documentElement
  useEffect(() => {
    const pref = snap?.preferences.appearance || "system";
    const mq = window.matchMedia("(prefers-color-scheme: dark)");

    const apply = () => {
      const resolved =
        pref === "system" ? (mq.matches ? "dark" : "light") : pref;
      document.documentElement.setAttribute("data-theme", resolved);
    };

    apply();
    mq.addEventListener("change", apply);
    return () => mq.removeEventListener("change", apply);
  }, [snap?.preferences.appearance]);

  if (!snap) {
    return (
      <div
        style={{
          width: "100%",
          height: "100%",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          color: "var(--text-secondary)",
          fontSize: 12.5,
        }}
      >
        XDial…
      </div>
    );
  }

  return view === "settings" ? (
    <SettingsView snap={snap} />
  ) : (
    <FlyoutView snap={snap} />
  );
};
