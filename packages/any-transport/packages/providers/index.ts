/**
 * YTP Providers — composable transport layer.
 *
 * Architecture:
 *   compose(channel, receiver, encoder) → Provider
 *
 * Components:
 *   Channel  — the API/platform you talk to   (6 platforms)
 *   Receiver — how you check for messages      (3 modes)
 *   Encoder  — how you pack data               (5 formats)
 *
 * Quick start:
 *   import { compose, VKChannel, TimerReceiver, TextEncoder } from './providers';
 *   const provider = compose(new VKChannel({...}), new TimerReceiver(), new TextEncoder());
 *
 * High bandwidth audio:
 *   import { compose, VKChannel, TimerReceiver, AudioEncoder } from './providers';
 *   const provider = compose(new VKChannel({...}), new TimerReceiver(), new AudioEncoder({ format: 'dvd' }));
 *
 * Cloud storage:
 *   import { compose, YandexDiskChannel, TimerReceiver, FileEncoder } from './providers';
 *   const provider = compose(new YandexDiskChannel({...}), new TimerReceiver(), new FileEncoder());
 *
 * Matrix:
 *   ┌──────────────────┐   ┌──────────────┐   ┌─────────────┐
 *   │    Channels       │   │   Receivers   │   │  Encoders   │
 *   ├──────────────────┤   ├──────────────┤   ├─────────────┤
 *   │ VKChannel        │   │ TimerRecv    │   │ TextEnc     │
 *   │ TGChannel        │   │ LongPollRecv │   │ DocEnc      │
 *   │ OKChannel        │   │ WebhookRecv  │   │ PhotoEnc    │
 *   │ YandexDiskCh     │   │              │   │ FileEnc     │
 *   │ MailRuCloudCh    │   │              │   │ AudioEnc 🆕 │
 *   │ SberCloudCh      │   │              │   │             │
 *   └──────────────────┘   └──────────────┘   └─────────────┘
 *
 * Encoder bandwidth comparison:
 *   TextEnc:    ~4 KB/msg
 *   DocEnc:     ~192 KB - 3 MB/msg (PNG pixels)
 *   AudioEnc:   ~187 KB - 50 MB/msg (WAV PCM) ← KING OF BANDWIDTH
 *
 * Combinations: 6 × 3 × 5 = 90 providers from 14 components!
 */

// ── Core types ───────────────────────────────────────────────────────────
export type { Provider, ProviderCursor, ProviderMessage, OutboundFrame, AppendResult, ProviderCapabilities, RateHint } from './provider';
export {
  LEGACY_PROVIDER_WIRE_PREFIX,
  LegacyProviderChannelAdapter,
  decodeChannelPayloadFromLegacyProvider,
  describeProviderBudget,
  describeProviderIdentity,
  encodeChannelPayloadForLegacyProvider,
} from './channel-contract';
export type { LegacyProviderChannelAdapterOptions, ProviderContractOptions } from './channel-contract';
export {
  ProviderControlPublishError,
  publishControlEnvelope,
  readControlEnvelopes,
} from './control-bus';
export type {
  ControlEnvelopeAnnouncement,
  ControlPublishResult,
  ProviderOperationFailure,
  PublishControlEnvelopeOptions,
  ReadControlEnvelopesOptions,
  ReadControlEnvelopesResult,
} from './control-bus';
export {
  createAdminCommandEnvelope,
  createClientFeedbackEnvelope,
  createProviderProbeEnvelope,
  createTransportEndpointEnvelope,
  publishAdminCommand,
  publishClientFeedback,
  publishProviderProbe,
  publishTransportEndpoint,
  readAdminCommands,
  readClientFeedback,
  readProviderProbes,
  readTransportEndpoints,
} from './control-helpers';
export type {
  CreateAdminCommandEnvelopeOptions,
  CreateClientFeedbackEnvelopeOptions,
  CreateProviderProbeEnvelopeOptions,
  CreateTransportEndpointEnvelopeOptions,
  PublishAdminCommandOptions,
  PublishClientFeedbackOptions,
  PublishProviderProbeOptions,
  PublishTransportEndpointOptions,
} from './control-helpers';
export {
  WbTunnelMessageType,
  WhitelistBypassTransport,
  assertWhitelistBypassEndpoint,
  createWhitelistBypassEndpoint,
} from './whitelist-bypass';
export type {
  CreateWhitelistBypassEndpointOptions,
  WbTunnelMessageTypeValue,
  WhitelistBypassEndpoint,
  WhitelistBypassTransportConfig,
  WhitelistBypassTransportSnapshot,
  WhitelistBypassTunnelMode,
} from './whitelist-bypass';
export {
  StreamDialError,
  StreamTransportDialer,
} from './stream-router';
export type {
  StreamDialFailure,
  StreamDialOptions,
  StreamDialResult,
  StreamRoute,
} from './stream-router';
export { VideoConferenceStreamTransport } from './video-conference';
export type { VideoConferenceStreamTransportOptions } from './video-conference';
export {
  RoomStatePublishError,
  createRoomStateEnvelope,
  publishRoomState,
  readRoomStates,
} from './room-discovery';
export type {
  CreateRoomStateEnvelopeOptions,
  PublishRoomStateOptions,
  ReadRoomStatesOptions,
  ReadRoomStatesResult,
  RoomStateAnnouncement,
  RoomStatePublishResult,
} from './room-discovery';

// ── Composable system ────────────────────────────────────────────────────
export { compose } from './compose';
export type { Channel, ChannelMessage, ChannelAttachment, ChannelCapabilities, Receiver, Encoder, ComposedProviderConfig } from './compose';

// ── Messaging Channels (VK, TG, OK) ─────────────────────────────────────
export { VKChannel } from './channel-vk';
export type { VKChannelConfig } from './channel-vk';
export { TGChannel } from './channel-tg';
export type { TGChannelConfig } from './channel-tg';
export { OKChannel } from './channel-ok';
export type { OKChannelConfig } from './channel-ok';

// ── Cloud Storage Channels ──────────────────────────────────────────────
export { YandexDiskChannel } from './channel-yandex-disk';
export type { YandexDiskChannelConfig } from './channel-yandex-disk';
export { MailRuCloudChannel } from './channel-mailru-cloud';
export type { MailRuCloudChannelConfig } from './channel-mailru-cloud';
export { SberCloudChannel } from './channel-sbercloud';
export type { SberCloudChannelConfig } from './channel-sbercloud';

// ── Receivers ────────────────────────────────────────────────────────────
export { TimerReceiver } from './receiver-timer';
export type { TimerReceiverConfig } from './receiver-timer';
export { LongPollReceiver } from './receiver-longpoll';
export type { LongPollReceiverConfig } from './receiver-longpoll';
export { WebhookReceiver, MemoryWebhookStore, handleVKWebhook, handleTGWebhook, handleOKWebhook } from './receiver-webhook';
export type { WebhookReceiverConfig, WebhookStore, StoredWebhookMessage } from './receiver-webhook';

// ── Encoders ─────────────────────────────────────────────────────────────
export { TextEncoder } from './encoder-text';
export type { TextEncoderConfig } from './encoder-text';
export { DocumentEncoder } from './encoder-doc';
export type { DocumentEncoderConfig } from './encoder-doc';
export { PhotoEncoder } from './encoder-photo';
export type { PhotoEncoderConfig } from './encoder-photo';
export { FileEncoder } from './encoder-file';
export type { FileEncoderConfig } from './encoder-file';
export { AudioEncoder } from './encoder-audio';
export type { AudioEncoderConfig } from './encoder-audio';

// ── Image codec ──────────────────────────────────────────────────────────
export {
  encodeToPixels, decodeFromPixels, splitIntoChunks, reassembleChunks,
  encodeToPNG, decodeFromPNG, encodeDataToPNG, decodeDataFromPNG,
  getImageStats, optimalImageSize,
} from './image-codec';

// ── Audio codec ──────────────────────────────────────────────────────────
export {
  encodeToPCM, decodeFromPCM, splitAudioIntoChunks, reassembleAudioChunks,
  encodeToWAV, decodeFromWAV, encodeDataToWAV, decodeDataFromWAV,
  getBytesPerSecond, getCapacity, getDurationForBytes, getAudioStats,
  optimalAudioFormat, AUDIO_FORMATS, DEFAULT_FORMAT,
} from './audio-codec';
export type { AudioFormat, EncodedAudio, DecodedAudio } from './audio-codec';

// ── FSK Modem (voice message steganography) ─────────────────────────────
export {
  fskEncode, fskDecode,
  float32ToPCM16, pcm16ToFloat32,
  applyFEC, removeFEC,
  getVoiceCapacity,
  FSK_MODES, DEFAULT_FSK_MODE,
} from './fsk-modem';
export type { FSKMode, FECLevel, FSKEncodeResult, FSKDecodeResult, VoiceCapacityInfo } from './fsk-modem';

// ── Legacy providers (deprecated — use compose() instead) ────────────────
export { MemoryProvider } from './memory';
export { FileProvider } from './file';
export { TelegramProvider } from './telegram';
export { VKProvider, VKMultiTokenProvider } from './vk';
export { VKPhotoProvider } from './vk-photo';
export { VKDocumentProvider } from './vk-document';
export { OKProvider } from './ok';
export { OKPhotoProvider } from './ok-photo';
export { OKDocumentProvider } from './ok-document';
export { YandexDiskProvider } from './yandex-disk';
export { VKBrowserBridgeProvider } from './vk-browser-bridge';
export {
  VKWebhookProvider,
  TGWebhookProvider,
  OKWebhookProvider,
} from './webhook';
export type {
  WebhookStore as LegacyWebhookStore,
  StoredWebhookMessage as LegacyStoredWebhookMessage,
  VKWebhookConfig,
  TGWebhookConfig,
  OKWebhookConfig,
} from './webhook';
