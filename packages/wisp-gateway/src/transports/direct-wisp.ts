import type { IncomingMessage } from "node:http";
import type { Duplex } from "node:stream";
import { server as wisp, logging } from "@mercuryworkshop/wisp-js/server";
import type { AppConfig } from "../config.js";
import type { UpgradeTransport } from "./index.js";

export class DirectWispTransport implements UpgradeTransport {
  readonly name = "direct-wisp";
  private upgrades = 0;

  constructor(private readonly config: AppConfig) {
    logging.set_level(logging.INFO);
    wisp.options.allow_udp_streams = config.allowUdp;
    wisp.options.allow_private_ips = config.allowPrivateIps;
    wisp.options.allow_loopback_ips = config.allowLoopbackIps;
    wisp.options.wisp_motd = config.motd;
  }

  routeUpgrade(req: IncomingMessage, socket: Duplex, head: Buffer): void {
    this.upgrades += 1;
    wisp.routeRequest(req, socket, head);
  }

  status(): Record<string, unknown> {
    return {
      name: this.name,
      upgrades: this.upgrades,
      allowUdp: this.config.allowUdp,
      allowPrivateIps: this.config.allowPrivateIps,
      allowLoopbackIps: this.config.allowLoopbackIps,
      motd: this.config.motd
    };
  }
}
