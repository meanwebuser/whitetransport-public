import type { AppConfig } from "../config.js";
import type { UpgradeTransport } from "./index.js";
import { DirectWispTransport } from "./direct-wisp.js";
import { WhitelistPlaceholderTransport } from "./whitelist-placeholder.js";
import { WhitelistSocksTransport } from "./whitelist-socks.js";

export function createTransport(config: AppConfig): UpgradeTransport {
  if (config.mode === "direct-wisp") return new DirectWispTransport(config);
  if (config.mode === "whitelist-socks") return new WhitelistSocksTransport(config);
  if (config.mode === "whitelist-placeholder") return new WhitelistPlaceholderTransport(config);
  throw new Error(`Unsupported WHITETRANSPORT_MODE: ${config.mode}`);
}
