import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  assertVideoConferenceProviderConfig,
  buildVideoConferenceProviderConfig,
  buildWbCreatorCommand,
  buildYtpEnvFile,
} from '../dist/index.js';

test('buildYtpEnvFile includes env sections for selected provider families', () => {
  const envFile = buildYtpEnvFile({
    serverName: 'NL-Amsterdam-01',
    mode: 'full',
    strategy: 'balanced',
    providers: ['vk-doc-256', 'tg-2bots', 'ok-text', 'yandex-disk'],
    envVars: {
      VK_TOKEN_1: 'vk-real-token',
      TG_CHAT_ID: '4242',
    },
  });

  assert.match(envFile, /# Server: NL-Amsterdam-01/);
  assert.match(envFile, /VK_TOKEN_1=vk-real-token/);
  assert.match(envFile, /TG_TOKEN_1=123456:ABC-DEF/);
  assert.match(envFile, /TG_TOKEN_2=789012:GHI-JKL/);
  assert.match(envFile, /TG_CHAT_ID=4242/);
  assert.match(envFile, /OK_TOKEN=your_token:APP_KEY/);
  assert.match(envFile, /YDISK_TOKEN=y0__your_token/);
  assert.match(envFile, /MODE=full$/);
});

test('buildVideoConferenceProviderConfig validates supported VP8 settings and disabled audio', () => {
  const config = buildVideoConferenceProviderConfig({
    providerId: 'vk-video-vp8',
    runtimeKind: 'adapter',
    role: 'joiner',
    roomSource: { kind: 'existing-room-url', roomUrl: 'https://vk.example/room' },
    vp8: { fps: 30, batch: 24, maxPacketBytes: 1400, targetBitrateKbps: 800 },
    audio: { enabled: false },
  });

  assert.equal(config.carrier, 'vp8');
  assert.equal(config.vp8?.fps, 30);
  assert.equal(config.audio.enabled, false);
  assert.doesNotThrow(() => assertVideoConferenceProviderConfig(config));
});

test('buildVideoConferenceProviderConfig rejects unsupported and reserved modes', () => {
  assert.throws(
    () => buildVideoConferenceProviderConfig({
      providerId: 'telemost-video-dualstream',
      runtimeKind: 'adapter',
      role: 'joiner',
      roomSource: { kind: 'existing-room-url', roomUrl: 'https://telemost.example/room' },
    }),
    /Unsupported video-conference provider telemost-video-dualstream/,
  );

  assert.throws(
    () => assertVideoConferenceProviderConfig({
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
      audio: { enabled: true },
    }),
    /Audio carrier is reserved/,
  );
});

test('buildWbCreatorCommand uses catalog command and replaces resource mode', () => {
  const command = buildWbCreatorCommand({
    platform: 'vk',
    resources: 'unlimited',
    cookiesPath: '/etc/wb/cookies-vk.json',
    writeFilePath: '/run/wb/call.txt',
  });

  assert.equal(
    command,
    './headless-vk-creator --cookies /etc/wb/cookies-vk.json --resources unlimited --write-file /run/wb/call.txt',
  );
});

test('buildWbCreatorCommand adds custom resource flags for VK', () => {
  const command = buildWbCreatorCommand({
    platform: 'vk',
    resources: 'custom',
    customResources: {
      readBuf: 65536,
      maxDcBuf: 8388608,
      memLimit: 268435456,
    },
  });

  assert.match(command, /--resources custom/);
  assert.match(command, /--read-buf 65536/);
  assert.match(command, /--max-dc-buf 8388608/);
  assert.match(command, /--mem-limit 268435456/);
});
