import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  VIDEO_CONFERENCE_PROVIDER_CATALOG,
  WB_PLATFORM_CATALOG,
  YTP_PROVIDER_CATALOG,
  findVideoConferenceProvider,
} from '../dist/index.js';

test('YTP provider catalog exposes known provider ids and valid strategies', () => {
  const providerIds = new Set([
    ...YTP_PROVIDER_CATALOG.text,
    ...YTP_PROVIDER_CATALOG.document,
    ...YTP_PROVIDER_CATALOG.photo,
  ].map((provider) => provider.id));

  assert.ok(providerIds.has('vk-text'));
  assert.ok(providerIds.has('tg-bot'));
  assert.ok(providerIds.has('ok-doc-256'));
  assert.ok(providerIds.has('yandex-disk'));

  for (const strategy of YTP_PROVIDER_CATALOG.strategies) {
    assert.ok(strategy.providers.length > 0, `${strategy.id} should list providers`);
    for (const providerId of strategy.providers) {
      assert.ok(providerIds.has(providerId), `${strategy.id} references unknown provider ${providerId}`);
    }
  }
});

test('video-conference catalog preserves distinct carriers and explicit unsupported modes', () => {
  const providers = new Map(VIDEO_CONFERENCE_PROVIDER_CATALOG.providers.map((provider) => [provider.id, provider]));

  assert.equal(providers.get('vk-video-datachannel')?.carrier, 'datachannel');
  assert.equal(providers.get('vk-video-vp8')?.carrier, 'vp8');
  assert.equal(providers.get('vk-video-dualstream')?.carrier, 'dualstream');
  assert.equal(providers.get('telemost-video-vp8')?.supported, true);
  assert.equal(providers.get('telemost-video-dualstream')?.supported, false);
  assert.ok(
    VIDEO_CONFERENCE_PROVIDER_CATALOG.unsupportedCombinations.some((item) => item.platform === 'telemost' && item.mode === 'datachannel'),
  );
  assert.equal(findVideoConferenceProvider('vk-video-vp8')?.runtimeKinds.includes('pion-relay'), true);
});

test('WB platform catalog exposes known platforms and valid tunnel modes', () => {
  const platformIds = new Set(WB_PLATFORM_CATALOG.platforms.map((platform) => platform.id));

  assert.deepEqual([...platformIds].sort(), ['telemost', 'vk', 'wbstream']);

  for (const tunnelMode of WB_PLATFORM_CATALOG.tunnelModes) {
    assert.ok(tunnelMode.platforms.length > 0, `${tunnelMode.id} should list platforms`);
    for (const platformId of tunnelMode.platforms) {
      assert.ok(platformIds.has(platformId), `${tunnelMode.id} references unknown platform ${platformId}`);
    }
  }
});
