import net from "node:net";
import { AsyncByteQueue } from "../lib/async-byte-queue.js";

export interface SocksTcpConfig {
  socksHost: string;
  socksPort: number;
  timeoutMs: number;
}

export interface SocksTcpMetrics {
  attempted: number;
  connected: number;
  failed: number;
  closed: number;
  bytesUp: number;
  bytesDown: number;
  lastError: string | null;
  lastTarget: string | null;
}

function onceEvent<T = void>(socket: net.Socket, event: string, timeoutMs: number): Promise<T> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => fail(new Error(`timeout waiting for ${event}`)), timeoutMs);

    const cleanup = (): void => {
      clearTimeout(timer);
      socket.off(event, onEvent as (...args: unknown[]) => void);
      socket.off("error", onError);
      socket.off("close", onClose);
    };

    const onEvent = (value: T) => {
      cleanup();
      resolve(value);
    };
    const onError = (error: Error) => fail(error);
    const onClose = () => fail(new Error(`socket closed before ${event}`));
    const fail = (error: Error): void => {
      cleanup();
      reject(error);
    };

    socket.once(event, onEvent as (...args: unknown[]) => void);
    socket.once("error", onError);
    socket.once("close", onClose);
  });
}

async function readExactly(socket: net.Socket, size: number, timeoutMs: number): Promise<Buffer> {
  const chunks: Buffer[] = [];
  let total = 0;

  return await new Promise<Buffer>((resolve, reject) => {
    const timer = setTimeout(() => fail(new Error(`timeout reading ${size} bytes from socks server`)), timeoutMs);

    const onData = (chunk: Buffer) => {
      chunks.push(chunk);
      total += chunk.length;
      if (total < size) return;
      const all = Buffer.concat(chunks, total);
      const wanted = all.subarray(0, size);
      const rest = all.subarray(size);
      done(wanted);
      if (rest.length > 0) socket.unshift(rest);
    };

    const onError = (error: Error) => fail(error);
    const onClose = () => fail(new Error("socket closed while reading socks response"));

    function cleanup(): void {
      clearTimeout(timer);
      socket.off("data", onData);
      socket.off("error", onError);
      socket.off("close", onClose);
    }

    function done(value: Buffer): void {
      cleanup();
      resolve(value);
    }

    function fail(error: Error): void {
      cleanup();
      reject(error);
    }

    socket.on("data", onData);
    socket.once("error", onError);
    socket.once("close", onClose);
  });
}

async function writeAll(socket: net.Socket, data: Buffer): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    socket.write(data, (error) => {
      if (error) reject(error);
      else resolve();
    });
  });
}

function socksReplyName(code: number): string {
  return {
    0x01: "general failure",
    0x02: "connection not allowed",
    0x03: "network unreachable",
    0x04: "host unreachable",
    0x05: "connection refused",
    0x06: "ttl expired",
    0x07: "command not supported",
    0x08: "address type not supported"
  }[code] ?? `unknown ${code}`;
}

export function createSocksTcpSocketClass(config: SocksTcpConfig, metrics: SocksTcpMetrics) {
  return class SocksTCPSocket {
    readonly hostname: string;
    readonly port: number;
    readonly recv_buffer_size = 128;
    readonly data_queue = new AsyncByteQueue(this.recv_buffer_size);
    socket: net.Socket | null = null;
    paused = false;
    connected = false;

    constructor(hostname: string, port: number) {
      this.hostname = hostname;
      this.port = port;
    }

    async connect(): Promise<void> {
      metrics.attempted += 1;
      metrics.lastTarget = `${this.hostname}:${this.port}`;

      const socket = new net.Socket();
      this.socket = socket;
      socket.setNoDelay(true);

      try {
        socket.connect({ host: config.socksHost, port: config.socksPort });
        await onceEvent(socket, "connect", config.timeoutMs);

        await writeAll(socket, Buffer.from([0x05, 0x01, 0x00]));
        const hello = await readExactly(socket, 2, config.timeoutMs);
        if (hello[0] !== 0x05 || hello[1] !== 0x00) {
          throw new Error(`SOCKS5 no-auth negotiation failed: ${hello.toString("hex")}`);
        }

        const hostnameBytes = Buffer.from(this.hostname, "utf8");
        if (hostnameBytes.length > 255) throw new Error("hostname is too long for SOCKS5 domain request");

        const request = Buffer.alloc(7 + hostnameBytes.length);
        request[0] = 0x05;
        request[1] = 0x01;
        request[2] = 0x00;
        request[3] = 0x03;
        request[4] = hostnameBytes.length;
        hostnameBytes.copy(request, 5);
        request.writeUInt16BE(this.port, 5 + hostnameBytes.length);
        await writeAll(socket, request);

        const header = await readExactly(socket, 4, config.timeoutMs);
        if (header[0] !== 0x05) throw new Error(`invalid SOCKS version in reply: ${header[0]}`);
        if (header[1] !== 0x00) throw new Error(`SOCKS connect failed: ${socksReplyName(header[1])}`);

        const atyp = header[3];
        if (atyp === 0x01) await readExactly(socket, 4 + 2, config.timeoutMs);
        else if (atyp === 0x03) {
          const len = await readExactly(socket, 1, config.timeoutMs);
          await readExactly(socket, len[0] + 2, config.timeoutMs);
        }
        else if (atyp === 0x04) await readExactly(socket, 16 + 2, config.timeoutMs);
        else throw new Error(`invalid SOCKS address type in reply: ${atyp}`);

        this.connected = true;
        metrics.connected += 1;

        socket.on("data", (data) => {
          metrics.bytesDown += data.length;
          this.data_queue.put(data);
        });
        socket.on("close", () => {
          metrics.closed += 1;
          this.data_queue.close();
          this.socket = null;
        });
        socket.on("error", (error) => {
          metrics.lastError = error.message;
          this.data_queue.close();
        });
      }
      catch (error) {
        metrics.failed += 1;
        metrics.lastError = error instanceof Error ? error.message : String(error);
        socket.destroy();
        this.socket = null;
        this.data_queue.close();
        throw error;
      }
    }

    async recv(): Promise<Buffer | null> {
      return await this.data_queue.get();
    }

    async send(data: Buffer | Uint8Array): Promise<void> {
      if (!this.socket) throw new Error("SOCKS TCP socket is not connected");
      const buffer = Buffer.isBuffer(data) ? data : Buffer.from(data);
      metrics.bytesUp += buffer.length;
      await writeAll(this.socket, buffer);
    }

    async close(): Promise<void> {
      if (!this.socket) return;
      this.socket.end();
      this.socket = null;
      this.data_queue.close();
    }

    pause(): void {
      if (!this.socket) return;
      if (this.data_queue.size >= this.data_queue.max_size) {
        this.socket.pause();
        this.paused = true;
      }
    }

    resume(): void {
      if (!this.socket) return;
      if (this.paused) {
        this.socket.resume();
        this.paused = false;
      }
    }
  };
}
