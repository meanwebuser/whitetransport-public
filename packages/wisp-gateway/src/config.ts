export type TransportMode = "direct-wisp" | "whitelist-socks" | "whitelist-placeholder";

export interface AppConfig {
  host: string;
  port: number;
  wispPath: string;
  mode: TransportMode;
  allowPrivateIps: boolean;
  allowLoopbackIps: boolean;
  allowUdp: boolean;
  motd: string;
  socksHost: string;
  socksPort: number;
  socksConnectTimeoutMs: number;
}

function boolFromEnv(name: string, fallback: boolean): boolean {
  const value = process.env[name];
  if (value == null || value === "") return fallback;
  return ["1", "true", "yes", "on"].includes(value.toLowerCase());
}

function intFromEnv(name: string, fallback: number): number {
  const raw = process.env[name];
  if (!raw) return fallback;
  const parsed = Number.parseInt(raw, 10);
  return Number.isFinite(parsed) ? parsed : fallback;
}

export function loadConfig(): AppConfig {
  const mode = (process.env.WHITETRANSPORT_MODE || "direct-wisp") as TransportMode;
  return {
    host: process.env.HOST || "127.0.0.1",
    port: intFromEnv("PORT", 5077),
    wispPath: process.env.WISP_PATH || "/wisp/",
    mode,
    allowPrivateIps: boolFromEnv("ALLOW_PRIVATE_IPS", false),
    allowLoopbackIps: boolFromEnv("ALLOW_LOOPBACK_IPS", false),
    allowUdp: boolFromEnv("ALLOW_UDP", false),
    motd: process.env.WISP_MOTD || "whitetransport",
    socksHost: process.env.WHITELIST_SOCKS_HOST || process.env.SOCKS_HOST || "127.0.0.1",
    socksPort: intFromEnv("WHITELIST_SOCKS_PORT", intFromEnv("SOCKS_PORT", 8809)),
    socksConnectTimeoutMs: intFromEnv("SOCKS_CONNECT_TIMEOUT_MS", 10000)
  };
}
