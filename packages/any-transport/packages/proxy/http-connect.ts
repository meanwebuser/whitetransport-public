/**
 * YTP HTTP CONNECT Proxy — local HTTP proxy for CONNECT tunneling.
 *
 * Handles HTTP CONNECT method to tunnel HTTPS connections.
 * Plain HTTP requests are not proxied in MVP (use SOCKS5 instead).
 */

import * as net from 'net';

export type HttpConnectHandler = (host: string, port: number, socket: net.Socket) => void;

export class HttpConnectServer {
  private server: net.Server | null = null;
  private handler: HttpConnectHandler;

  constructor(handler: HttpConnectHandler) {
    this.handler = handler;
  }

  async listen(host = '127.0.0.1', port = 8080): Promise<void> {
    return new Promise((resolve, reject) => {
      this.server = net.createServer((socket) => {
        this.handleConnection(socket).catch(err => {
          console.error('[HTTP-CONNECT] Connection error:', err);
          socket.destroy();
        });
      });

      this.server.on('error', reject);
      this.server.listen(port, host, () => {
        console.log(`[HTTP-CONNECT] Listening on ${host}:${port}`);
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
    const header = await this.readHeader(socket);
    if (!header) {
      socket.destroy();
      return;
    }

    if (header.method === 'CONNECT') {
      // Tunnel mode
      socket.write('HTTP/1.1 200 Connection Established\r\n\r\n');
      this.handler(header.host, header.port, socket);
    } else {
      // Plain HTTP not supported in MVP
      socket.write('HTTP/1.1 501 Not Implemented\r\nContent-Length: 0\r\n\r\n');
      socket.end();
    }
  }

  private readHeader(socket: net.Socket): Promise<{ method: string; host: string; port: number } | null> {
    return new Promise((resolve) => {
      let buffer = '';

      const onData = (data: Buffer) => {
        buffer += data.toString('utf-8');

        // Check if we have the full header
        const headerEnd = buffer.indexOf('\r\n\r\n');
        if (headerEnd === -1) {
          // Not yet, keep reading
          if (buffer.length > 8192) {
            // Header too large
            socket.removeListener('data', onData);
            resolve(null);
          }
          return;
        }

        socket.removeListener('data', onData);
        const headerStr = buffer.slice(0, headerEnd);
        const firstLine = headerStr.split('\r\n')[0];

        // Parse: METHOD URI HTTP/VERSION
        const parts = firstLine.split(' ');
        if (parts.length < 3) {
          resolve(null);
          return;
        }

        const method = parts[0];

        if (method === 'CONNECT') {
          // URI is host:port
          const target = parts[1];
          const [host, portStr] = target.split(':');
          const port = parseInt(portStr, 10) || 443;
          resolve({ method, host, port });
        } else {
          resolve({ method, host: '', port: 0 });
        }
      };

      socket.on('data', onData);
      socket.on('error', () => resolve(null));
    });
  }
}
