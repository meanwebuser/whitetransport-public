import type { IncomingMessage } from "node:http";
import type { Duplex } from "node:stream";
import type { AppConfig } from "../config.js";
import type { UpgradeTransport } from "./index.js";

export class WhitelistPlaceholderTransport implements UpgradeTransport {
  readonly name = "whitelist-placeholder";
  private rejectedUpgrades = 0;

  constructor(private readonly config: AppConfig) {}

  routeUpgrade(_req: IncomingMessage, socket: Duplex, _head: Buffer): void {
    this.rejectedUpgrades += 1;
    socket.write("HTTP/1.1 501 Not Implemented\r\nConnection: close\r\nContent-Type: text/plain\r\n\r\nwhitelist-bypass transport is not implemented yet\n");
    socket.destroy();
  }

  status(): Record<string, unknown> {
    return {
      name: this.name,
      rejectedUpgrades: this.rejectedUpgrades,
      note: "Placeholder for whitelist-bypass/WebRTC transport bridge.",
      configuredWispPath: this.config.wispPath
    };
  }
}
