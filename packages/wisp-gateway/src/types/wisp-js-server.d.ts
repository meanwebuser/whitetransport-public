declare module "@mercuryworkshop/wisp-js/server" {
  import type { IncomingMessage } from "node:http";
  import type { Duplex } from "node:stream";

  export const server: {
    options: Record<string, any>;
    routeRequest(req: IncomingMessage, socket: Duplex, head: Buffer, connOptions?: Record<string, any>): void;
  };

  export const logging: {
    DEBUG: number;
    INFO: number;
    WARN: number;
    ERROR: number;
    NONE: number;
    set_level(level: number): void;
  };
}
