import type { IncomingMessage } from "node:http";
import type { Duplex } from "node:stream";
import { server as wisp, logging } from "@mercuryworkshop/wisp-js/server";
import type { AppConfig } from "../config.js";
import type { UpgradeTransport } from "./index.js";
import { createSocksTcpSocketClass, type SocksTcpMetrics } from "./socks-tcp-socket.js";

export class WhitelistSocksTransport implements UpgradeTransport {
  readonly name = "whitelist-socks";
  private upgrades = 0;
  private readonly metrics: SocksTcpMetrics = {
    attempted: 0,
    connected: 0,
    failed: 0,
    closed: 0,
    bytesUp: 0,
    bytesDown: 0,
    lastError: null,
    lastTarget: null
  };
  private readonly TCPSocket: ReturnType<typeof createSocksTcpSocketClass>;

  constructor(private readonly config: AppConfig) {
    logging.set_level(logging.INFO);
    wisp.options.allow_udp_streams = false;
    wisp.options.allow_private_ips = config.allowPrivateIps;
    wisp.options.allow_loopback_ips = config.allowLoopbackIps;
    wisp.options.wisp_motd = config.motd || "whitetransport whitelist-socks";
    this.TCPSocket = createSocksTcpSocketClass({
      socksHost: config.socksHost,
      socksPort: config.socksPort,
      timeoutMs: config.socksConnectTimeoutMs
    }, this.metrics);
  }

  routeUpgrade(req: IncomingMessage, socket: Duplex, head: Buffer): void {
    this.upgrades += 1;
    wisp.routeRequest(req, socket, head, {
      TCPSocket: this.TCPSocket,
      UDPSocket: undefined
    });
  }

  status(): Record<string, unknown> {
    return {
      name: this.name,
      upgrades: this.upgrades,
      socks: {
        host: this.config.socksHost,
        port: this.config.socksPort,
        timeoutMs: this.config.socksConnectTimeoutMs
      },
      udp: "disabled",
      metrics: this.metrics
    };
  }
}
