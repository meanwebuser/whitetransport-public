import { execFile } from 'node:child_process';
import net from 'node:net';
import path from 'node:path';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);

interface SocksProbeResult {
  readonly externalIp: string;
  readonly responseStatus: string;
}

interface BufferedSocketReader {
  readExactly(length: number): Promise<Buffer>;
  readToEnd(): Promise<Buffer>;
}

export function parseSocksPortFromRuntimeConfig(configJson: string): number {
  const config = JSON.parse(configJson) as { socks_listen?: unknown };
  if (typeof config.socks_listen !== 'string') throw new Error('embedded runtime config has no socks_listen');
  const port = Number(config.socks_listen.slice(config.socks_listen.lastIndexOf(':') + 1));
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`embedded runtime config has invalid socks_listen: ${config.socks_listen}`);
  }
  return port;
}

async function androidSocksPort(): Promise<number> {
  const configured = process.env.WT_E2E_ANDROID_SOCKS_PORT;
  if (configured) {
    const port = Number(configured);
    if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error(`invalid WT_E2E_ANDROID_SOCKS_PORT: ${configured}`);
    return port;
  }
  const apk = process.env.WT_E2E_APK;
  if (!apk) return 1080;
  const result = await execFileAsync('unzip', ['-p', path.resolve(apk), 'assets/wt-runtime-config.json'], { timeout: 30_000 });
  return parseSocksPortFromRuntimeConfig(result.stdout);
}

function androidSerial(): string | undefined {
  return process.env.WT_E2E_ANDROID_UDID
    ?? process.env.WT_E2E_ANDROID_SERIAL
    ?? process.env.ANDROID_SERIAL;
}

function adbPath(): string {
  const androidHome = process.env.ANDROID_HOME ?? process.env.ANDROID_SDK_ROOT;
  return androidHome ? path.join(androidHome, 'platform-tools', 'adb') : 'adb';
}

async function adb(args: readonly string[]): Promise<string> {
  const serial = androidSerial();
  const serialArgs = serial ? ['-s', serial] : [];
  const result = await execFileAsync(adbPath(), [...serialArgs, ...args], { timeout: 30_000 });
  return result.stdout.trim();
}

function bufferedReader(socket: net.Socket): BufferedSocketReader {
  let buffered = Buffer.alloc(0);
  let ended = false;
  let failure: Error | undefined;
  const waiters = new Set<() => void>();
  const notify = (): void => {
    for (const waiter of waiters) waiter();
    waiters.clear();
  };

  socket.on('data', (chunk: Buffer) => {
    buffered = Buffer.concat([buffered, chunk]);
    notify();
  });
  socket.on('end', () => {
    ended = true;
    notify();
  });
  socket.on('error', (error: Error) => {
    failure = error;
    notify();
  });

  const waitForChange = async (): Promise<void> => {
    if (ended || failure) return;
    await new Promise<void>((resolve) => waiters.add(resolve));
  };

  return {
    async readExactly(length: number): Promise<Buffer> {
      while (buffered.length < length && !ended && !failure) await waitForChange();
      if (failure) throw failure;
      if (buffered.length < length) throw new Error(`SOCKS response ended after ${buffered.length}/${length} bytes`);
      const value = buffered.subarray(0, length);
      buffered = buffered.subarray(length);
      return value;
    },
    async readToEnd(): Promise<Buffer> {
      while (!ended && !failure) await waitForChange();
      if (failure) throw failure;
      const value = buffered;
      buffered = Buffer.alloc(0);
      return value;
    },
  };
}

async function connectSocket(port: number): Promise<net.Socket> {
  return new Promise<net.Socket>((resolve, reject) => {
    const socket = net.createConnection({ host: '127.0.0.1', port });
    socket.setTimeout(60_000, () => socket.destroy(new Error('Android SOCKS payload probe timed out')));
    socket.once('connect', () => resolve(socket));
    socket.once('error', reject);
  });
}

async function runSocksHttpProbe(port: number): Promise<SocksProbeResult> {
  const targetHost = process.env.WT_E2E_ANDROID_PROBE_HOST ?? 'api.ipify.org';
  const targetPort = Number(process.env.WT_E2E_ANDROID_PROBE_PORT ?? '80');
  const targetPath = process.env.WT_E2E_ANDROID_PROBE_PATH ?? '/?format=text';
  const hostBytes = Buffer.from(targetHost, 'utf8');
  if (hostBytes.length > 255) throw new Error('Android SOCKS probe hostname is too long');

  const socket = await connectSocket(port);
  const reader = bufferedReader(socket);
  try {
    socket.write(Buffer.from([0x05, 0x01, 0x00]));
    const auth = await reader.readExactly(2);
    if (auth[0] !== 0x05 || auth[1] !== 0x00) throw new Error(`SOCKS authentication failed: ${auth.toString('hex')}`);

    const request = Buffer.alloc(7 + hostBytes.length);
    request.set([0x05, 0x01, 0x00, 0x03, hostBytes.length], 0);
    hostBytes.copy(request, 5);
    request.writeUInt16BE(targetPort, 5 + hostBytes.length);
    socket.write(request);

    const reply = await reader.readExactly(4);
    if (reply[0] !== 0x05 || reply[1] !== 0x00) throw new Error(`SOCKS connect failed with code ${reply[1]}`);
    const addressLength = reply[3] === 0x01 ? 4 : reply[3] === 0x04 ? 16 : (await reader.readExactly(1))[0];
    await reader.readExactly(addressLength + 2);

    socket.write(`GET ${targetPath} HTTP/1.1\r\nHost: ${targetHost}\r\nConnection: close\r\nUser-Agent: WhiteTransport-Appium-Android\r\n\r\n`);
    const response = (await reader.readToEnd()).toString('utf8');
    const [headers = '', body = ''] = response.split(/\r?\n\r?\n/, 2);
    const status = headers.split(/\r?\n/, 1)[0] ?? '';
    const externalIp = body.trim();
    if (!/^HTTP\/1\.[01] 2\d\d\b/.test(status)) throw new Error(`SOCKS HTTP probe returned ${status || 'no status'}`);
    if (!/^[0-9a-f:.]+$/i.test(externalIp)) throw new Error(`SOCKS HTTP probe returned an invalid IP body: ${externalIp.slice(0, 120)}`);
    return { externalIp, responseStatus: status };
  } finally {
    socket.destroy();
  }
}

/** Proves payload transfer through the SOCKS listener owned by the Android app. */
export async function probeAndroidSocksPayload(): Promise<SocksProbeResult> {
  const devicePort = await androidSocksPort();
  const localPortText = await adb(['forward', 'tcp:0', `tcp:${devicePort}`]);
  const localPort = Number(localPortText);
  if (!Number.isInteger(localPort) || localPort < 1) throw new Error(`adb forward returned invalid port: ${localPortText}`);

  try {
    return await runSocksHttpProbe(localPort);
  } finally {
    await adb(['forward', '--remove', `tcp:${localPort}`]).catch(() => undefined);
  }
}
