/**
 * YTP AudioEncoder — encode data as audio via document attachments.
 *
 * THREE MODES:
 *
 *   Mode 'doc' (default) — Upload WAV as DOCUMENT attachment
 *     Highest bandwidth: 192 KB/s — 384 KB/s per message
 *     Data packed directly into PCM samples, every byte = data
 *     ⚠️  MUST upload as document, NOT voice message!
 *     VK/TG re-encode voice → Opus destroys raw PCM
 *
 *   Mode 'voice' — Upload as voice message with FSK modem encoding
 *     Stealth mode: looks like a normal voice message
 *     FSK tones survive Opus compression (300-3400 Hz band)
 *     Very low bandwidth: ~12-50 bytes/sec after FEC
 *     Use when document uploads are blocked/restricted
 *
 *   Mode 'auto' — Try 'doc' first, fall back to 'voice'
 *
 * Bandwidth comparison:
 *   ┌─────────────────────────────────┬──────────────┬───────────────┐
 *   │ Mode                            │ Per message  │ Per 10 sec    │
 *   ├─────────────────────────────────┼──────────────┼───────────────┤
 *   │ TextEncoder                     │ 4 KB         │ 4 KB          │
 *   │ DocumentEncoder (256² PNG)      │ 192 KB       │ 192 KB        │
 *   │ AudioEncoder 'doc' DVD 10s      │ 187 KB       │ 187 KB        │
 *   │ AudioEncoder 'doc' studio 10s   │ 375 KB       │ 375 KB        │
 *   │ AudioEncoder 'voice' FSK300 10s │ 125 B        │ 125 B         │
 *   │ AudioEncoder 'voice' FSK1200 10s│ 500 B        │ 500 B         │
 *   └─────────────────────────────────┴──────────────┴───────────────┘
 *
 * VK voice messages specifically:
 *   - Codec: Opus @ ~16-32 kbps, mono, in OGG container
 *   - VK re-encodes server-side → raw PCM data destroyed
 *   - FSK tones in 300-3400 Hz survive Opus compression
 *   - Theoretical max with FSK300+FEC: ~1.5 KB per 2-min voice
 *   - Theoretical max with FSK1200+FEC: ~6 KB per 2-min voice
 *   - Compare to doc upload: 50 MB — voice is 1000-30000x slower
 *   - BUT: voice messages look normal → high stealth
 */

import type { Channel, Encoder } from './compose';
import type { OutboundFrame, AppendResult, ProviderMessage } from './provider';
import {
  encodeDataToWAV,
  decodeDataFromWAV,
  splitAudioIntoChunks,
  getCapacity,
  getBytesPerSecond,
  getDurationForBytes,
  AUDIO_FORMATS,
  DEFAULT_FORMAT,
} from './audio-codec';
import type { AudioFormat } from './audio-codec';
import {
  fskEncode,
  fskDecode,
  float32ToPCM16,
  pcm16ToFloat32,
  applyFEC,
  removeFEC,
  getVoiceCapacity,
  FSK_MODES,
  DEFAULT_FSK_MODE,
} from './fsk-modem';
import type { FECLevel, FSKMode } from './fsk-modem';
import { deflateSync, inflateSync } from 'zlib';

export type AudioTransportMode = 'doc' | 'voice' | 'auto';

export interface AudioEncoderConfig {
  /** Transport mode: 'doc' (WAV as document), 'voice' (FSK in voice message), 'auto'. Default: 'doc' */
  transportMode?: AudioTransportMode;
  /** Audio format preset name or custom format (for 'doc' mode). Default: 'dvd' */
  format?: string | AudioFormat;
  /** Duration of each audio chunk in seconds (for 'doc' mode). Default: auto */
  durationSec?: number;
  /** Compress payload with zlib before encoding (for 'doc' mode). Default: true */
  compress?: boolean;
  /** FSK mode name (for 'voice' mode). Default: 'robust300' */
  fskMode?: string;
  /** FEC level (for 'voice' mode). Default: 'triple' */
  fecLevel?: FECLevel;
  label?: string;
}

export class AudioEncoder implements Encoder {
  readonly id: string;

  private format: AudioFormat;
  private config: AudioEncoderConfig;
  private compress: boolean;
  private transportMode: AudioTransportMode;
  private fskMode: string;
  private fecLevel: FECLevel;

  constructor(config: AudioEncoderConfig = {}) {
    this.config = config;
    this.compress = config.compress ?? true;
    this.transportMode = config.transportMode ?? 'doc';
    this.fskMode = config.fskMode ?? DEFAULT_FSK_MODE;
    this.fecLevel = config.fecLevel ?? 'triple';

    // Resolve format
    if (config.format) {
      if (typeof config.format === 'string') {
        this.format = AUDIO_FORMATS[config.format] || AUDIO_FORMATS['dvd'];
      } else {
        this.format = config.format;
      }
    } else {
      this.format = DEFAULT_FORMAT;
    }

    this.id = config.label ? `audio-${config.label}` : 'audio';
  }

  async encode(frame: OutboundFrame, channel: Channel): Promise<AppendResult> {
    if (this.transportMode === 'voice') {
      return this.encodeVoice(frame, channel);
    }

    if (this.transportMode === 'auto') {
      // Try doc mode first, fall back to voice
      if (channel.uploadDocument) {
        return this.encodeDoc(frame, channel);
      }
      return this.encodeVoice(frame, channel);
    }

    // Default: doc mode
    return this.encodeDoc(frame, channel);
  }

  async decode(raw: import('./compose').ChannelMessage, channel: Channel): Promise<ProviderMessage> {
    // Check if message has document/voice attachments
    if (raw.attachments.length > 0 && channel.downloadAttachment) {
      for (const att of raw.attachments) {
        if (att.type === 'doc') {
          try {
            const buffer = await channel.downloadAttachment(att);

            // Try to decode as WAV (doc mode)
            try {
              const decoded = decodeDataFromWAV(buffer);
              if (decoded.crcOk) {
                let payload = decoded.payload;
                if (this.compress) {
                  try { payload = inflateSync(payload); } catch {}
                }
                return {
                  id: raw.id,
                  timestamp: raw.timestamp,
                  text: payload.toString('utf-8'),
                  fromSelf: raw.fromSelf,
                };
              }
            } catch {
              // Not a YTP WAV
            }

            // Try to decode as FSK voice message
            try {
              const pcm = this.wavToPCM16(buffer);
              const samples = pcm16ToFloat32(pcm);
              const mode = FSK_MODES[this.fskMode] || FSK_MODES[DEFAULT_FSK_MODE];
              const decoded = fskDecode(samples, mode, this.fecLevel);
              if (decoded.crcOk) {
                return {
                  id: raw.id,
                  timestamp: raw.timestamp,
                  text: decoded.payload.toString('utf-8'),
                  fromSelf: raw.fromSelf,
                };
              }
            } catch {
              // Not FSK voice either
            }
          } catch {
            // Download failed
          }
        }
      }
    }

    // No decodable attachment — return text as-is
    return {
      id: raw.id,
      timestamp: raw.timestamp,
      text: raw.text,
      fromSelf: raw.fromSelf,
    };
  }

  maxPayloadBytes(): number {
    if (this.transportMode === 'voice') {
      const cap = getVoiceCapacity(this.fskMode, this.fecLevel);
      const duration = this.config.durationSec ?? 120; // 2 min voice
      return Math.floor(cap.effectiveBytesPerSec * duration - cap.headerOverheadBytes);
    }

    const duration = this.config.durationSec ?? 10;
    const rawCapacity = getCapacity(duration, this.format);
    return this.compress ? Math.floor(rawCapacity * 0.9) : rawCapacity;
  }

  // ── Doc mode: WAV as document ──────────────────────────────────────────

  private async encodeDoc(frame: OutboundFrame, channel: Channel): Promise<AppendResult> {
    if (!channel.uploadDocument) {
      console.warn(`[AudioEncoder:${this.id}] No doc upload, falling back to text`);
      return this.encodeViaText(frame, channel);
    }

    let payload = Buffer.from(frame.text, 'utf-8');

    // Optionally compress
    if (this.compress) {
      payload = deflateSync(payload);
    }

    // Calculate duration
    const duration = this.config.durationSec ?? getDurationForBytes(payload.length, this.format);
    const capacity = getCapacity(duration, this.format);

    if (payload.length <= capacity) {
      // Single chunk
      const wavBuffer = encodeDataToWAV(payload, this.format, 0, 1, duration);
      const filename = `ytp_audio_${Date.now()}.wav`;

      const attachment = await channel.uploadDocument(wavBuffer, filename);
      const result = await channel.sendMessage('[YTP:AUD:0/1]', attachment);
      return { messageId: result.messageId, timestamp: result.timestamp };
    }

    // Multi-chunk
    const chunkDuration = this.config.durationSec ?? 10;
    const chunks = splitAudioIntoChunks(payload, this.format, chunkDuration);
    let lastResult: AppendResult | null = null;

    for (let i = 0; i < chunks.length; i++) {
      const wavBuffer = this.pcmToWAV(chunks[i]);
      const filename = `ytp_audio_${Date.now()}_${i}.wav`;

      const attachment = await channel.uploadDocument(wavBuffer, filename);
      const result = await channel.sendMessage(
        `[YTP:AUD:${i}/${chunks.length}]`,
        attachment,
      );

      lastResult = { messageId: result.messageId, timestamp: result.timestamp };

      if (i < chunks.length - 1) {
        await this.sleep(channel.caps().minSendIntervalMs);
      }
    }

    return lastResult!;
  }

  // ── Voice mode: FSK in voice message ───────────────────────────────────

  private async encodeVoice(frame: OutboundFrame, channel: Channel): Promise<AppendResult> {
    if (!channel.uploadDocument) {
      console.warn(`[AudioEncoder:${this.id}] No doc upload for voice FSK, falling back to text`);
      return this.encodeViaText(frame, channel);
    }

    const payload = Buffer.from(frame.text, 'utf-8');
    const mode = FSK_MODES[this.fskMode] || FSK_MODES[DEFAULT_FSK_MODE];

    // Apply FEC (repetition coding)
    const fecPayload = applyFEC(payload, this.fecLevel);

    // Encode as FSK audio tones
    const fskResult = fskEncode(fecPayload, mode);

    // Convert Float32 to 16-bit PCM
    const pcm16 = float32ToPCM16(fskResult.samples);

    // Wrap in WAV container (48kHz, 16-bit, mono)
    const wavBuffer = this.buildVoiceWAV(pcm16, fskResult.durationSec);

    const filename = `ytp_voice_${Date.now()}.wav`;

    // Upload as document (or as voice message if channel supports it)
    const attachment = await channel.uploadDocument(wavBuffer, filename);
    const result = await channel.sendMessage(
      `[YTP:VOX:0/1:${fskResult.durationSec.toFixed(1)}s]`,
      attachment,
    );

    console.log(`[AudioEncoder:${this.id}] FSK voice: ${payload.length}B → ${fskResult.durationSec.toFixed(1)}s audio, mode=${this.fskMode}, fec=${this.fecLevel}`);

    return { messageId: result.messageId, timestamp: result.timestamp };
  }

  // ── Private helpers ────────────────────────────────────────────────────

  private pcmToWAV(audio: import('./audio-codec').EncodedAudio): Buffer {
    const { format, data: pcmData } = audio;
    const byteRate = getBytesPerSecond(format);
    const blockAlign = (format.bitDepth / 8) * format.channels;
    const dataSize = pcmData.length;

    const riffHeader = Buffer.alloc(12);
    riffHeader.write('RIFF', 0);
    riffHeader.writeUInt32LE(36 + dataSize, 4);
    riffHeader.write('WAVE', 8);

    const fmtChunk = Buffer.alloc(24);
    fmtChunk.write('fmt ', 0);
    fmtChunk.writeUInt32LE(16, 4);
    fmtChunk.writeUInt16LE(1, 8);
    fmtChunk.writeUInt16LE(format.channels, 10);
    fmtChunk.writeUInt32LE(format.sampleRate, 12);
    fmtChunk.writeUInt32LE(byteRate, 16);
    fmtChunk.writeUInt16LE(blockAlign, 20);
    fmtChunk.writeUInt16LE(format.bitDepth, 22);

    const dataHeader = Buffer.alloc(8);
    dataHeader.write('data', 0);
    dataHeader.writeUInt32LE(dataSize, 4);

    return Buffer.concat([riffHeader, fmtChunk, dataHeader, pcmData]);
  }

  private buildVoiceWAV(pcm16Data: Buffer, durationSec: number): Buffer {
    // 48kHz, 16-bit, mono WAV
    const sampleRate = 48000;
    const channels = 1;
    const bitDepth = 16;
    const byteRate = sampleRate * channels * (bitDepth / 8);
    const blockAlign = channels * (bitDepth / 8);
    const dataSize = pcm16Data.length;

    const riffHeader = Buffer.alloc(12);
    riffHeader.write('RIFF', 0);
    riffHeader.writeUInt32LE(36 + dataSize, 4);
    riffHeader.write('WAVE', 8);

    const fmtChunk = Buffer.alloc(24);
    fmtChunk.write('fmt ', 0);
    fmtChunk.writeUInt32LE(16, 4);
    fmtChunk.writeUInt16LE(1, 8);
    fmtChunk.writeUInt16LE(channels, 10);
    fmtChunk.writeUInt32LE(sampleRate, 12);
    fmtChunk.writeUInt32LE(byteRate, 16);
    fmtChunk.writeUInt16LE(blockAlign, 20);
    fmtChunk.writeUInt16LE(bitDepth, 22);

    const dataHeader = Buffer.alloc(8);
    dataHeader.write('data', 0);
    dataHeader.writeUInt32LE(dataSize, 4);

    return Buffer.concat([riffHeader, fmtChunk, dataHeader, pcm16Data]);
  }

  private wavToPCM16(wavBuffer: Buffer): Buffer {
    try {
      const { decodeFromWAV } = require('./audio-codec');
      const decoded = decodeFromWAV(wavBuffer);
      // Convert Float32 or raw PCM to 16-bit PCM
      // audio-codec decodeFromWAV returns raw PCM bytes
      return decoded.pcmData;
    } catch {
      // Try extracting PCM data from WAV directly
      if (wavBuffer.slice(0, 4).toString() !== 'RIFF') {
        throw new Error('Not a WAV file');
      }

      let offset = 12;
      while (offset < wavBuffer.length - 8) {
        const chunkId = wavBuffer.slice(offset, offset + 4).toString();
        const chunkSize = wavBuffer.readUInt32LE(offset + 4);

        if (chunkId === 'data') {
          return wavBuffer.slice(offset + 8, offset + 8 + chunkSize);
        }

        offset += 8 + chunkSize;
        if (chunkSize % 2 !== 0) offset++;
      }

      throw new Error('No data chunk in WAV');
    }
  }

  private async encodeViaText(frame: OutboundFrame, channel: Channel): Promise<AppendResult> {
    const maxBytes = channel.caps().maxTextBytes - 20;
    const chunks: string[] = [];

    if (frame.text.length <= maxBytes) {
      chunks.push(frame.text);
    } else {
      let offset = 0;
      let partIdx = 0;
      while (offset < frame.text.length) {
        const header = `[YTP:${partIdx}] `;
        const chunkSize = Math.min(maxBytes - header.length, frame.text.length - offset);
        chunks.push(header + frame.text.slice(offset, offset + chunkSize));
        offset += chunkSize;
        partIdx++;
      }
    }

    let lastResult: AppendResult | null = null;
    for (let i = 0; i < chunks.length; i++) {
      const result = await channel.sendMessage(chunks[i]);
      lastResult = { messageId: result.messageId, timestamp: result.timestamp };
      if (i < chunks.length - 1) await this.sleep(channel.caps().minSendIntervalMs);
    }

    return lastResult!;
  }

  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}
