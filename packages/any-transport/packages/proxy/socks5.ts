/**
 * YTP SOCKS5 Proxy — local SOCKS5 server that tunnels TCP CONNECT
 * through the Y Transport protocol.
 *
 * MVP: TCP CONNECT only (no UDP, no BIND, no raw IP).
 *
 * Flow:
 *   Browser -> 127.0.0.1:1080
 *   Electron parses SOCKS5 CONNECT request
 *   YTP OPEN stream with target=example.com:443
 *   Remote node opens TCP socket
 *   DATA frames flow bidirectionally
 */

import * as net from 'net';

// ── SOCKS5 constants ───────────────────────────────────────────────────
const SOCKS_VERSION = 0x05;
const AUTH_NONE = 0x00;
const AUTH_NO_ACCEPTABLE = 0xFF;
const CMD_CONNECT = 0x01;
const ATYP_IPV4 = 0x01;
const ATYP_DOMAIN = 0x03;
const ATYP_IPV6 = 0x04;
const REP_SUCCESS = 0x00;
const REP_GENERAL_FAILURE = 0x01;
const REP_NOT_ALLOWED = 0x02;
const REP_NETWORK_UNREACHABLE = 0x03;
const REP_HOST_UNREACHABLE = 0x04;
const REP_CONNECTION_REFUSED = 0x05;
const REP_COMMAND_NOT_SUPPORTED = 0x07;
const REP_ATYPE_NOT_SUPPORTED = 0x08;

export interface Socks5Request {
  targetHost: string;
  targetPort: number;
}

export type Socks5Handler = (request: Socks5Request, socket: net.Socket) => void;

export class Socks5Server {
  private server: net.Server | null = null;
  private handler: Socks5Handler;

  constructor(handler: Socks5Handler) {
    this.handler = handler;
  }

  async listen(host = '127.0.0.1', port = 1080): Promise<void> {
    return new Promise((resolve, reject) => {
      this.server = net.createServer((socket) => {
        this.handleConnection(socket).catch(err => {
          console.error('[SOCKS5] Connection error:', err);
          socket.destroy();
        });
      });

      this.server.on('error', reject);
      this.server.listen(port, host, () => {
        console.log(`[SOCKS5] Listening on ${host}:${port}`);
        resolve();
      });
    });
  }

  async close(): Promise<void> {
    return new Promise((resolve) => {
      if (this.server) {
        this.server.close(() => resolve());
      } else {
        resolve();
      }
    });
  }

  private async handleConnection(socket: net.Socket): Promise<void> {
    // Phase 1: Auth negotiation
    const authMethod = await this.negotiateAuth(socket);
    if (authMethod === null) {
      socket.destroy();
      return;
    }

    // Phase 2: Request
    const request = await this.readRequest(socket);
    if (request === null) {
      socket.destroy();
      return;
    }

    // Only support CONNECT
    // (already handled in readRequest)

    // Delegate to handler
    this.handler(request, socket);
  }

  private negotiateAuth(socket: net.Socket): Promise<number | null> {
    return new Promise((resolve) => {
      const onData = (data: Buffer) => {
        socket.removeListener('data', onData);

        if (data[0] !== SOCKS_VERSION) {
          resolve(null);
          return;
        }

        const nMethods = data[1];
        const methods = data.slice(2, 2 + nMethods);

        if (methods.includes(AUTH_NONE)) {
          // Reply: version + no-auth
          socket.write(Buffer.from([SOCKS_VERSION, AUTH_NONE]));
          resolve(AUTH_NONE);
        } else {
          socket.write(Buffer.from([SOCKS_VERSION, AUTH_NO_ACCEPTABLE]));
          resolve(null);
        }
      };

      socket.once('data', onData);
      socket.on('error', () => resolve(null));
    });
  }

  private readRequest(socket: net.Socket): Promise<Socks5Request | null> {
    return new Promise((resolve) => {
      const onData = (data: Buffer) => {
        socket.removeListener('data', onData);

        if (data.length < 7 || data[0] !== SOCKS_VERSION) {
          resolve(null);
          return;
        }

        const cmd = data[1];
        if (cmd !== CMD_CONNECT) {
          this.sendReply(socket, REP_COMMAND_NOT_SUPPORTED);
          resolve(null);
          return;
        }

        const atyp = data[3];
        let targetHost: string;
        let portOffset: number;

        if (atyp === ATYP_IPV4) {
          targetHost = `${data[4]}.${data[5]}.${data[6]}.${data[7]}`;
          portOffset = 8;
        } else if (atyp === ATYP_DOMAIN) {
          const domainLen = data[4];
          targetHost = data.slice(5, 5 + domainLen).toString('ascii');
          portOffset = 5 + domainLen;
        } else if (atyp === ATYP_IPV6) {
          // IPv6 not supported in MVP
          this.sendReply(socket, REP_ATYPE_NOT_SUPPORTED);
          resolve(null);
          return;
        } else {
          this.sendReply(socket, REP_ATYPE_NOT_SUPPORTED);
          resolve(null);
          return;
        }

        const targetPort = data.readUInt16BE(portOffset);
        resolve({ targetHost, targetPort });
      };

      socket.once('data', onData);
      socket.on('error', () => resolve(null));
    });
  }

  private sendReply(socket: net.Socket, rep: number): void {
    const reply = Buffer.alloc(10);
    reply[0] = SOCKS_VERSION;
    reply[1] = rep;
    reply[2] = 0x00; // reserved
    reply[3] = ATYP_IPV4;
    // 4-7: bound address (0.0.0.0)
    // 8-9: bound port (0)
    socket.write(reply);
  }

  /** Send a successful CONNECT reply */
  static sendSuccessReply(socket: net.Socket): void {
    const reply = Buffer.alloc(10);
    reply[0] = SOCKS_VERSION;
    reply[1] = REP_SUCCESS;
    reply[2] = 0x00;
    reply[3] = ATYP_IPV4;
    reply[4] = 127;
    reply[5] = 0;
    reply[6] = 0;
    reply[7] = 1;
    // port = 0
    socket.write(reply);
  }
}
