import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  RuntimeVideoConferenceAdapter,
  buildVkVp8RuntimeCommands,
  redactRuntimeCommand,
} from '../dist/index.js';

function createConfig() {
  return {
    schemaVersion: 1,
    providerId: 'vk-video-vp8',
    platform: 'vk',
    mode: 'vp8',
    carrier: 'vp8',
    runtimeKind: 'adapter',
    role: 'joiner',
    roomSource: { kind: 'existing-room-url', roomUrl: 'https://vk.example/room' },
    browserHookMode: 'inject-vk-hook',
    pionRelay: { enabled: true, listenHost: '127.0.0.1', listenPort: 9001, iceTransportPolicy: 'relay' },
    vp8: { fps: 24, batch: 30, trackCount: 1, maxPacketBytes: 1200 },
    audio: { enabled: false },
  };
}

function createLauncher() {
  const launched = [];
  return {
    launched,
    async launch(command) {
      launched.push(command);
      return {
        pid: 1000 + launched.length,
        command,
        exited: new Promise(() => {}),
        async stop() {},
      };
    },
  };
}

function createDuplex() {
  const chunks = [];
  return {
    async write(chunk) {
      chunks.push(new Uint8Array(chunk));
    },
    async read() {
      return chunks.shift() ?? null;
    },
    async close() {
      chunks.length = 0;
    },
  };
}

test('buildVkVp8RuntimeCommands builds create, join, and Pion relay commands', () => {
  const commands = buildVkVp8RuntimeCommands({
    headlessCreatorPath: './headless-vk-creator',
    cookiesPath: '/run/secrets/cookies-vk.json',
    resources: 'default',
    workingDirectory: '/opt/whitelist-bypass',
    roomOutputPath: '/run/wt/vk-room.txt',
    existingRoomUrl: 'https://vk.com/call/join/token',
    pionRelayPath: './pion-relay',
    pionPort: 9101,
  });

  assert.deepEqual(commands.createRoom?.args, [
    '--cookies',
    '/run/secrets/cookies-vk.json',
    '--resources',
    'default',
    '--write-file',
    '/run/wt/vk-room.txt',
  ]);
  assert.deepEqual(commands.joinRoom?.args, [
    '--cookies',
    '/run/secrets/cookies-vk.json',
    '--resources',
    'default',
    '--vk-link',
    'https://vk.com/call/join/token',
  ]);
  assert.deepEqual(commands.pionRelay?.args, ['--platform', 'vk', '--port', '9101']);
});

test('RuntimeVideoConferenceAdapter launches legacy commands through injected launcher', async () => {
  const launcher = createLauncher();
  const config = createConfig();
  const adapter = new RuntimeVideoConferenceAdapter({
    providerId: 'vk-video-vp8',
    launcher,
    commands: buildVkVp8RuntimeCommands({
      headlessCreatorPath: './headless-vk-creator',
      cookiesPath: '/run/secrets/cookies-vk.json',
      roomOutputPath: '/run/wt/vk-room.txt',
      existingRoomUrl: 'https://vk.example/room',
      pionRelayPath: './pion-relay',
    }),
    roomUrlReader: async () => 'https://vk.example/created-room',
    streamConnector: async () => createDuplex(),
    now: () => 2000,
  });

  const room = await adapter.createRoom({ config, roomId: 'vk-room-a' });
  const stream = await adapter.openStream({ config, room });
  await stream.duplex.write(new Uint8Array([4, 5, 6]));

  const status = await adapter.getRuntimeStatus();
  assert.equal(room.url, 'https://vk.example/created-room');
  assert.equal(launcher.launched.length, 2);
  assert.equal(launcher.launched[0].executable, './headless-vk-creator');
  assert.equal(launcher.launched[1].executable, './pion-relay');
  assert.deepEqual(await stream.duplex.read(), new Uint8Array([4, 5, 6]));
  assert.equal(status.runtimeProcesses?.length, 2);
  assert.equal(status.activeStreamIds.length, 1);
});

test('redactRuntimeCommand hides sensitive environment values', () => {
  const command = redactRuntimeCommand({
    executable: './runtime',
    args: [],
    env: {
      VK_TOKEN: 'secret-token',
      SAFE_VALUE: 'visible',
    },
    sensitiveEnvKeys: ['VK_TOKEN'],
  });

  assert.equal(command.env?.VK_TOKEN, '[redacted]');
  assert.equal(command.env?.SAFE_VALUE, 'visible');
});
