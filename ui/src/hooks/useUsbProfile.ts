import { useEffect, useState } from "react";
import { useJsonRpc } from "./useJsonRpc";
import { useSettingsStore } from "./stores";

// Profile comes from the current device connection, never browser persistence.
// Until it is known, input stays disabled rather than using stale device state.
export function isRestrictedRelativeProfile(profile: string | null) {
  return profile === "standard-relative" || profile === "receiver-enumeration";
}

export function useUsbProfile() {
  const { send } = useJsonRpc();
  const { mouseMode: preference, setMouseMode } = useSettingsStore();
  const [profile, setProfile] = useState<string | null>(null);
  useEffect(() => {
    let active = true;
    setProfile(null);
    send("getUsbDevices", {}, response => {
      if (!active || "error" in response) return;
      const devices = response.result as { hid_profile?: string };
      const value = devices.hid_profile ?? ""; // Older upstream is legacy.
      setProfile(value);
      if (isRestrictedRelativeProfile(value)) setMouseMode("relative");
    });
    return () => {
      active = false;
    };
  }, [send, setMouseMode]);
  const standard = isRestrictedRelativeProfile(profile);
  return {
    profile,
    standard,
    mouseMode: standard ? "relative" : profile === "" ? preference : "disabled",
  };
}
