import { describe, expect, test } from '@jest/globals';
import { buildVideoConferenceProviderConfig } from '@whitetransport/provider-channels';
import { MemoryVideoConferenceAdapter } from '@whitetransport/video-conference-transport';
import { StreamTransportDialer } from './stream-router';
import { VideoConferenceStreamTransport } from './video-conference';

describe('VideoConferenceStreamTransport', () => {
  test('opens fake adapter streams through StreamTransportDialer', async () => {
    const config = buildVideoConferenceProviderConfig({
      providerId: 'vk-video-vp8',
      runtimeKind: 'adapter',
      role: 'joiner',
      roomSource: { kind: 'existing-room-url', roomUrl: 'https://vk.example/room' },
    });
    const adapter = new MemoryVideoConferenceAdapter({ now: () => 1000 });
    const transport = new VideoConferenceStreamTransport({ config, adapter });
    const dialer = new StreamTransportDialer([{ channel: transport, endpoint: transport.createEndpoint(), priority: 1 }]);

    const result = await dialer.connect({ providerId: 'vk-video-vp8' });
    await result.stream.write(new Uint8Array([7, 8, 9]));

    expect(result.route.channel.identity.kind).toBe('video-conference');
    expect(await result.stream.read()).toEqual(new Uint8Array([7, 8, 9]));
    expect((await adapter.getRuntimeStatus()).activeStreamIds).toHaveLength(1);
  });
});
