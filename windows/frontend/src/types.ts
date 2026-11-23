export type Phase =
  | "disconnected"
  | "connecting"
  | "connected"
  | "switching"
  | "disconnecting"
  | "failed";

export type IngressMode = "system-proxy" | "wintun" | "local-socks";

export type LineType =
  | "direct"
  | "selector"
  | "urltest"
  | "vpn"
  | "trojan"
  | "shadowsocks"
  | "vmess"
  | "vless"
  | "anytls"
  | "tailscale";

export interface Line {
  id: string;
  name: string;
  type: LineType;
  enabled: boolean;
  identity_profile_id?: string;
  identity_hostname?: string;

  // Group fields
  group_members?: string[];
  group_default?: string;
  group_url?: string;
  group_interval?: string;

  // VPN (AnyConnect via sslcon)
  vpn_server?: string;
  vpn_username?: string;
  vpn_password?: string;

  // Trojan
  trojan_server?: string;
  trojan_port?: number;
  trojan_password?: string;
  trojan_sni?: string;

  // Shadowsocks
  ss_server?: string;
  ss_port?: number;
  ss_method?: string;
  ss_password?: string;

  // VMess
  vmess_server?: string;
  vmess_port?: number;
  vmess_uuid?: string;
  vmess_alt_id?: number;

  // VLESS (+ Reality / TLS)
  vless_server?: string;
  vless_port?: number;
  vless_uuid?: string;
  vless_flow?: string;
  vless_sni?: string;
  vless_client_fingerprint?: string;
  vless_alpn?: string[];
  vless_reality_public_key?: string;
  vless_reality_short_id?: string;
  vless_packet_encoding?: string;

  // AnyTLS
  anytls_server?: string;
  anytls_port?: number;
  anytls_password?: string;
  anytls_sni?: string;
  anytls_client_fingerprint?: string;
  anytls_alpn?: string[];
  anytls_idle_session_check_interval?: number;
  anytls_idle_session_timeout?: number;
  anytls_min_idle_session?: number;

  // Tailscale
  tailscale_exit_node?: string;
  tailscale_magic_dns?: boolean;

  // Common
  tfo?: boolean;
  udp?: boolean;
  allow_insecure?: boolean;
}

export type RuleSetType = "url" | "native" | "manual" | "application" | "group";

export interface RuleSet {
  id: string;
  name: string;
  type: RuleSetType;
  enabled: boolean;
  invert?: boolean;
  no_resolve?: boolean;
  url?: string;
  format?: string;
  fetch_line_id?: string;
  domains?: string[];
  cidrs?: string[];
  processes?: string[];
}

export interface RuleBinding {
  rule_set_id: string;
  line_id?: string;
  condition_ids?: string[];
}

export interface Scenario {
  id: string;
  name: string;
  icon?: string;
  match_ssids?: string[];
  match_order?: string[];
  bindings: RuleBinding[];
  default_line_id: string;
}

export interface Preferences {
  ingress_mode: IngressMode;
  also_expose_local_socks: boolean;
  socks_port: number;
  automatic_scenario: boolean;
  launch_at_login: boolean;
  connect_on_launch: boolean;
  notify_disconnect: boolean;
  notify_scenario_switch: boolean;
  notify_network_change: boolean;
  appearance: "system" | "light" | "dark";
  language: "zh-CN" | "en";
  show_tray_speed: boolean;
  receive_beta: boolean;
  onboarding_dismissed?: boolean;
}

export type TaskState =
  | "pending"
  | "running"
  | "ready"
  | "committing"
  | "committed"
  | "rolling_back"
  | "rolled_back"
  | "failed"
  | "skipped";

export interface TaskProgress {
  id: string;
  kind: string;
  name: string;
  detail?: string;
  state: TaskState;
}

export interface ActiveLineStatus {
  id: string;
  name: string;
  type: string;
  exit_ipv4?: string;
  exit_ipv6?: string;
  latency_ms?: number;
  testing?: boolean;
  error?: string;
}

export interface RouteMappingRow {
  rule_id?: string;
  rule_name: string;
  line_id: string;
  line_name: string;
  latency_ms?: number;
  is_default?: boolean;
}

export interface SwitchFailureInfo {
  from_scenario: string;
  to_scenario: string;
  message: string;
}

export interface FailureDetails {
  title: string;
  raw_error: string;
  ingress_fact: string;
  rollback_fact: string;
  rollback_ok: boolean;
}

export interface LatencyRecord {
  milliseconds?: number;
  error?: string;
  testing?: boolean;
  observed_at?: number;
}

export interface ProfileSourceItem {
  id: string;
  name: string;
  is_subscription: boolean;
  remote_url?: string;
}

export interface EditorView {
  editing_source_id: string;
  editing_source_name: string;
  sources: ProfileSourceItem[];
  lines: Line[];
  line_groups: Line[];
  rule_sets: RuleSet[];
  scenarios: Scenario[];
  active_scenario_id: string;
  all_lines: Line[];
  all_rule_sets: RuleSet[];
}

export interface UnderlaySnapshot {
  default_interface_name: string;
  default_interface_index: number;
  default_ipv4?: string;
  dns_servers: string[];
  connected_ssid?: string;
  is_elevated: boolean;
  has_wintun_dll: boolean;
}

export interface AppSnapshot {
  phase: Phase;
  target_scenario_name?: string;
  active_ingress_mode?: IngressMode;
  active_socks_port?: number;
  is_proxy_only: boolean;
  connected_at?: number;
  download_bps?: number;
  upload_bps?: number;
  config_dirty: boolean;
  needs_sign_in: boolean;
  fresh_unconfigured: boolean;
  blocker_message?: string;
  auto_reason?: string;
  parked_recovery: boolean;
  switch_failure?: SwitchFailureInfo;
  failure?: FailureDetails;
  tasks?: TaskProgress[];
  active_lines?: ActiveLineStatus[];
  route_mappings?: RouteMappingRow[];
  editor: EditorView;
  latencies: Record<string, LatencyRecord>;
  preferences: Preferences;
  system_facts: UnderlaySnapshot;
  version: string;
}
