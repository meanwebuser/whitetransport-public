import type { IncomingMessage } from "node:http";
import type { Duplex } from "node:stream";

export interface UpgradeTransport {
  readonly name: string;
  routeUpgrade(req: IncomingMessage, socket: Duplex, head: Buffer): void;
  status(): Record<string, unknown>;
}
