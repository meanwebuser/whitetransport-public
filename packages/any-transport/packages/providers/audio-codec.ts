/**
 * YTP AudioCodec — Encode arbitrary binary data into WAV audio and decode back.
 *
 * Strategy: Pack raw bytes directly into PCM sample values.
 * Every single bit of every sample carries data — 100% capacity utilization.
 *
 * WAV is lossless and preserves all sample data when uploaded as DOCUMENT.
 * DO NOT upload as voice message — VK/TG re-encode to Opus → data destroyed.
 *
 * Header format (first 48 bytes = first 24 samples at 16-bit stereo):
 *   Bytes 0-3:   Magic "YTA1" (YTP Audio v1)
 *   Bytes 4-7:   Payload length (uint32 LE)
 *   Bytes 8-11:  Chunk index (uint32 LE)
 *   Bytes 12-15: Total chunks (uint32 LE)
 *   Bytes 16-19: CRC32 of payload (uint32 LE)
 *   Bytes 20-23: Sample rate (uint32 LE)
 *   Bytes 24-27: Bit depth (uint16 LE) + Channels (uint16 LE)
 *   Bytes 28-31: Duration seconds (uint32 LE, for verification)
 *   Bytes 32-47: Reserved (zeros)
 *
 * Capacity comparison:
 *   ┌─────────────────────────────────┬──────────────┬───────────────┐
 *   │ Format                          │ Bytes/sec    │ 10-sec total  │
 *   ├─────────────────────────────────┼──────────────┼───────────────┤
 *   │ WAV 8kHz, 8-bit, mono          │  8,000       │   7.8 KB      │
 *   │ WAV 44.1kHz, 16-bit, mono      │ 88,200       │  86.1 KB      │
 *   │ WAV 48kHz, 16-bit, mono        │ 96,000       │  93.8 KB      │
 *   │ WAV 48kHz, 16-bit, stereo      │ 192,000      │ 187.5 KB      │
 *   │ WAV 48kHz, 24-bit, stereo      │ 288,000      │ 281.3 KB      │
 *   │ WAV 48kHz, 32-bit, stereo      │ 384,000      │ 375.0 KB      │
 *   │ WAV 96kHz, 32-bit, stereo      │ 768,000      │ 750.0 KB      │
 *   │ WAV 192kHz, 32-bit, stereo     │ 1,536,000    │ 1.5 GB        │
 *   ├─────────────────────────────────┼──────────────┼───────────────┤
 *   │ VK/TG doc limit = 50MB         │              │               │
 *   │ → at 48kHz/16bit/stereo:       │ ~273 sec     │  ~4.5 min     │
 *   │ → at 48kHz/32bit/stereo:       │ ~137 sec     │  ~2.3 min     │
 *   │ → at 96kHz/32bit/stereo:       │ ~68 sec      │  ~1.1 min     │
 *   ├─────────────────────────────────┼──────────────┼───────────────┤
 *   │ PNG 256×256 (image codec)      │              │ 192 KB total  │
 *   │ PNG 1024×1024 (image codec)    │              │   3 MB total  │
 *   │ WAV 10s/48kHz/16bit/stereo     │              │ 187 KB total  │
 *   │ WAV 30s/48kHz/16bit/stereo     │              │ 562 KB total  │
 *   │ WAV 60s/48kHz/16bit/stereo     │              │ 1.1 MB total  │
 *   │ WAV 10s/48kHz/32bit/stereo     │              │ 375 KB total  │
 *   │ WAV 60s/48kHz/32bit/stereo     │              │ 2.25 MB total │
 *   └─────────────────────────────────┴──────────────┴───────────────┘
 *
 * Audio is MUCH better than PNG for large payloads because:
 *   1. Linear structure = no wasted space (vs PNG padding, filter bytes, alpha)
 *   2. No compression overhead — raw PCM is the payload
 *   3. Scales smoothly with duration (add 1 sec = add 96-384 KB)
 *   4. VK/TG accept large audio documents (50MB)
 *
 * WARNING: Must upload as DOCUMENT, NOT as voice message!
 *   ✅ channel.uploadDocument(wavBuffer, 'audio.wav')
 *   ❌ channel.uploadPhoto(wavBuffer, 'audio.wav') — will be re-encoded!
 */

import { randomBytes } from 'crypto';
import { deflateSync, inflateSync } from 'zlib';

// ── Constants ────────────────────────────────────────────────────────────

const MAGIC = Buffer.from('YTA1');
const HEADER_SIZE = 48;

// ── WAV Format Structures ────────────────────────────────────────────────

export interface AudioFormat {
  sampleRate: number;     // 8000, 44100, 48000, 96000, 192000
  bitDepth: number;       // 8, 16, 24, 32
  channels: number;       // 1 (mono), 2 (stereo)
}

export const AUDIO_FORMATS: Record<string, AudioFormat> = {
  'voice':     { sampleRate: 8000,  bitDepth: 16, channels: 1 }, // 8 KB/s — minimal
  'telephone': { sampleRate: 16000, bitDepth: 16, channels: 1 }, // 32 KB/s
  'radio':     { sampleRate: 22050, bitDepth: 16, channels: 1 }, // 44.1 KB/s
  'cd-mono':   { sampleRate: 44100, bitDepth: 16, channels: 1 }, // 88.2 KB/s
  'cd-stereo': { sampleRate: 44100, bitDepth: 16, channels: 2 }, // 176.4 KB/s
  'dvd':       { sampleRate: 48000, bitDepth: 16, channels: 2 }, // 192 KB/s
  'hq':        { sampleRate: 48000, bitDepth: 24, channels: 2 }, // 288 KB/s
  'studio':    { sampleRate: 48000, bitDepth: 32, channels: 2 }, // 384 KB/s
  'pro':       { sampleRate: 96000, bitDepth: 32, channels: 2 }, // 768 KB/s
  'max':       { sampleRate: 192000, bitDepth: 32, channels: 2 },// 1.5 MB/s
};

export const DEFAULT_FORMAT: AudioFormat = AUDIO_FORMATS['dvd']; // 192 KB/s — good balance

// ── Encoded Audio ────────────────────────────────────────────────────────

export interface EncodedAudio {
  format: AudioFormat;
  durationSeconds: number;
  data: Buffer;          // raw PCM sample data (interleaved)
  payloadSize: number;
  chunkIndex: number;
  totalChunks: number;
}

// ── Decoded Audio ────────────────────────────────────────────────────────

export interface DecodedAudio {
  payload: Buffer;
  chunkIndex: number;
  totalChunks: number;
  crcOk: boolean;
}

// ── Calculate capacity ──────────────────────────────────────────────────

export function getBytesPerSecond(format: AudioFormat): number {
  return format.sampleRate * (format.bitDepth / 8) * format.channels;
}

export function getCapacity(durationSec: number, format: AudioFormat = DEFAULT_FORMAT): number {
  return Math.floor(durationSec * getBytesPerSecond(format)) - HEADER_SIZE;
}

export function getDurationForBytes(payloadBytes: number, format: AudioFormat = DEFAULT_FORMAT): number {
  const bps = getBytesPerSecond(format);
  return Math.ceil((payloadBytes + HEADER_SIZE) / bps);
}

export function getAudioStats(format: AudioFormat = DEFAULT_FORMAT, durationSec: number = 10) {
  const bps = getBytesPerSecond(format);
  const totalBytes = durationSec * bps;
  const maxPayload = totalBytes - HEADER_SIZE;
  const wavOverhead = 44; // WAV header

  return {
    format,
    bytesPerSecond: bps,
    bytesPerSecondKB: Math.floor(bps / 1024),
    durationSec,
    totalPCMBytes: totalBytes,
    maxPayloadPerChunk: maxPayload,
    maxPayloadKB: Math.floor(maxPayload / 1024),
    wavFileSize: totalBytes + wavOverhead,
    wavFileSizeKB: Math.floor((totalBytes + wavOverhead) / 1024),
    headerSize: HEADER_SIZE,
  };
}

// ── Encode: data → PCM samples ───────────────────────────────────────────

export function encodeToPCM(
  payload: Buffer,
  format: AudioFormat = DEFAULT_FORMAT,
  chunkIndex: number = 0,
  totalChunks: number = 1,
  durationSeconds?: number,
): EncodedAudio {
  const bps = getBytesPerSecond(format);
  const minDuration = Math.ceil((payload.length + HEADER_SIZE) / bps);
  const duration = durationSeconds ?? minDuration;
  const totalPCMBytes = duration * bps;
  const maxPayload = totalPCMBytes - HEADER_SIZE;

  if (payload.length > maxPayload) {
    throw new Error(
      `Payload too large: ${payload.length} bytes > ${maxPayload} bytes ` +
      `(for ${duration}s at ${format.sampleRate}Hz/${format.bitDepth}bit/${format.channels}ch)`
    );
  }

  // Build header
  const header = Buffer.alloc(HEADER_SIZE);
  MAGIC.copy(header, 0);                           // Magic "YTA1"
  header.writeUInt32LE(payload.length, 4);          // Payload length
  header.writeUInt32LE(chunkIndex, 8);              // Chunk index
  header.writeUInt32LE(totalChunks, 12);            // Total chunks
  const crcVal = crc32(payload);
  header.writeUInt32LE(crcVal, 16);                 // CRC32
  header.writeUInt32LE(format.sampleRate, 20);      // Sample rate
  header.writeUInt16LE(format.bitDepth, 24);        // Bit depth
  header.writeUInt16LE(format.channels, 26);        // Channels
  header.writeUInt32LE(duration, 28);               // Duration

  // Combine header + payload
  const dataBuffer = Buffer.concat([header, payload]);

  // Pad remaining PCM bytes with random noise (sounds like white noise — natural)
  if (dataBuffer.length < totalPCMBytes) {
    const padding = randomBytes(totalPCMBytes - dataBuffer.length);
    const padded = Buffer.concat([dataBuffer, padding]);
    return {
      format,
      durationSeconds: duration,
      data: padded,
      payloadSize: payload.length,
      chunkIndex,
      totalChunks,
    };
  }

  return {
    format,
    durationSeconds: duration,
    data: dataBuffer,
    payloadSize: payload.length,
    chunkIndex,
    totalChunks,
  };
}

// ── Decode: PCM samples → data ───────────────────────────────────────────

export function decodeFromPCM(pcmData: Buffer, format?: AudioFormat): DecodedAudio {
  // Try to read header from the PCM data
  if (pcmData.length < HEADER_SIZE) {
    throw new Error(`PCM data too short: ${pcmData.length} < ${HEADER_SIZE}`);
  }

  const magic = pcmData.slice(0, 4).toString();
  if (magic !== 'YTA1') {
    throw new Error(`Invalid magic: ${magic} (expected YTA1)`);
  }

  const payloadLen = pcmData.readUInt32LE(4);
  const chunkIndex = pcmData.readUInt32LE(8);
  const totalChunks = pcmData.readUInt32LE(12);
  const expectedCrc = pcmData.readUInt32LE(16);
  const storedSampleRate = pcmData.readUInt32LE(20);
  const storedBitDepth = pcmData.readUInt16LE(24);
  const storedChannels = pcmData.readUInt16LE(26);
  const storedDuration = pcmData.readUInt32LE(28);

  // Verify payload fits in data
  if (payloadLen > pcmData.length - HEADER_SIZE) {
    throw new Error(`Invalid payload length: ${payloadLen} > available ${pcmData.length - HEADER_SIZE}`);
  }

  const payload = pcmData.slice(HEADER_SIZE, HEADER_SIZE + payloadLen);

  // Verify CRC
  const actualCrc = crc32(payload);
  const crcOk = actualCrc === expectedCrc;

  return {
    payload,
    chunkIndex,
    totalChunks,
    crcOk,
  };
}

// ── Split data into multiple audio chunks ─────────────────────────────────

export function splitAudioIntoChunks(
  data: Buffer,
  format: AudioFormat = DEFAULT_FORMAT,
  durationPerChunk: number = 10,
): EncodedAudio[] {
  const maxPayload = getCapacity(durationPerChunk, format);
  const totalChunks = Math.ceil(data.length / maxPayload);

  const chunks: EncodedAudio[] = [];
  for (let i = 0; i < totalChunks; i++) {
    const start = i * maxPayload;
    const end = Math.min(start + maxPayload, data.length);
    const chunk = data.slice(start, end);

    chunks.push(encodeToPCM(chunk, format, i, totalChunks, durationPerChunk));
  }

  return chunks;
}

// ── Reassemble chunks ────────────────────────────────────────────────────

export function reassembleAudioChunks(decoded: DecodedAudio[]): Buffer {
  const sorted = [...decoded].sort((a, b) => a.chunkIndex - b.chunkIndex);

  const totalChunks = sorted[0]?.totalChunks || 0;
  if (sorted.length !== totalChunks) {
    throw new Error(`Missing chunks: have ${sorted.length}, expected ${totalChunks}`);
  }

  for (const chunk of sorted) {
    if (!chunk.crcOk) {
      throw new Error(`CRC mismatch in chunk ${chunk.chunkIndex}`);
    }
  }

  return Buffer.concat(sorted.map(c => c.payload));
}

// ── WAV file encoder (minimal, no external deps) ─────────────────────────
// Creates a valid WAV file from EncodedAudio.
// WAV is just: RIFF header + fmt chunk + data chunk + PCM samples.

export function encodeToWAV(audio: EncodedAudio): Buffer {
  const { format, data: pcmData } = audio;
  const byteRate = getBytesPerSecond(format);
  const blockAlign = (format.bitDepth / 8) * format.channels;
  const dataSize = pcmData.length;

  // RIFF header (12 bytes)
  const riffHeader = Buffer.alloc(12);
  riffHeader.write('RIFF', 0);                        // ChunkID
  riffHeader.writeUInt32LE(36 + dataSize, 4);          // ChunkSize (file - 8)
  riffHeader.write('WAVE', 8);                         // Format

  // fmt sub-chunk (24 bytes)
  const fmtChunk = Buffer.alloc(24);
  fmtChunk.write('fmt ', 0);                           // Subchunk1ID
  fmtChunk.writeUInt32LE(16, 4);                       // Subchunk1Size (PCM = 16)
  fmtChunk.writeUInt16LE(1, 8);                        // AudioFormat (1 = PCM)
  fmtChunk.writeUInt16LE(format.channels, 10);         // NumChannels
  fmtChunk.writeUInt32LE(format.sampleRate, 12);       // SampleRate
  fmtChunk.writeUInt32LE(byteRate, 16);                // ByteRate
  fmtChunk.writeUInt16LE(blockAlign, 20);              // BlockAlign
  fmtChunk.writeUInt16LE(format.bitDepth, 22);         // BitsPerSample

  // data sub-chunk header (8 bytes) + PCM data
  const dataHeader = Buffer.alloc(8);
  dataHeader.write('data', 0);                         // Subchunk2ID
  dataHeader.writeUInt32LE(dataSize, 4);               // Subchunk2Size

  return Buffer.concat([riffHeader, fmtChunk, dataHeader, pcmData]);
}

// ── WAV file decoder ─────────────────────────────────────────────────────

export interface DecodedWAV {
  format: AudioFormat;
  durationSeconds: number;
  pcmData: Buffer;
}

export function decodeFromWAV(wavBuffer: Buffer): DecodedWAV {
  // Verify RIFF header
  if (wavBuffer.slice(0, 4).toString() !== 'RIFF') {
    throw new Error('Not a valid WAV file: missing RIFF header');
  }
  if (wavBuffer.slice(8, 12).toString() !== 'WAVE') {
    throw new Error('Not a valid WAV file: missing WAVE format');
  }

  let format: AudioFormat = { ...DEFAULT_FORMAT };
  let pcmData: Buffer = Buffer.alloc(0);
  let offset = 12; // Skip RIFF header

  // Parse sub-chunks
  while (offset < wavBuffer.length - 8) {
    const chunkId = wavBuffer.slice(offset, offset + 4).toString();
    const chunkSize = wavBuffer.readUInt32LE(offset + 4);

    if (chunkId === 'fmt ') {
      const audioFormat = wavBuffer.readUInt16LE(offset + 8);
      if (audioFormat !== 1) {
        throw new Error(`Unsupported WAV format: ${audioFormat} (only PCM=1 supported)`);
      }
      format = {
        channels: wavBuffer.readUInt16LE(offset + 10),
        sampleRate: wavBuffer.readUInt32LE(offset + 12),
        bitDepth: wavBuffer.readUInt16LE(offset + 22),
      };
    } else if (chunkId === 'data') {
      pcmData = wavBuffer.slice(offset + 8, offset + 8 + chunkSize);
      break; // Data chunk found, stop parsing
    }

    offset += 8 + chunkSize;
    // Align to even boundary
    if (chunkSize % 2 !== 0) offset++;
  }

  if (pcmData.length === 0) {
    throw new Error('WAV file has no data chunk');
  }

  const bps = getBytesPerSecond(format);
  const durationSeconds = pcmData.length / bps;

  return { format, durationSeconds, pcmData };
}

// ── Full pipeline: binary data → WAV buffer ──────────────────────────────

export function encodeDataToWAV(
  data: Buffer,
  format: AudioFormat = DEFAULT_FORMAT,
  chunkIndex: number = 0,
  totalChunks: number = 1,
  durationSeconds?: number,
): Buffer {
  const audio = encodeToPCM(data, format, chunkIndex, totalChunks, durationSeconds);
  return encodeToWAV(audio);
}

// ── Full pipeline: WAV buffer → binary data ──────────────────────────────

export function decodeDataFromWAV(wavBuffer: Buffer): DecodedAudio {
  const { pcmData } = decodeFromWAV(wavBuffer);
  return decodeFromPCM(pcmData);
}

// ── Auto-optimize: choose best format for a given payload size ───────────

export function optimalAudioFormat(
  payloadBytes: number,
  targetDurationSec: number = 10,
  maxFileSize: number = 50 * 1024 * 1024, // 50MB default (VK/TG limit)
): { format: AudioFormat; durationSec: number; wavFileSize: number } {
  // Try formats from smallest to largest until we find one that fits
  const candidates = Object.values(AUDIO_FORMATS);

  for (const format of candidates) {
    const bps = getBytesPerSecond(format);
    const duration = Math.max(targetDurationSec, Math.ceil((payloadBytes + HEADER_SIZE) / bps));
    const wavSize = duration * bps + 44; // 44 = WAV header overhead

    if (wavSize <= maxFileSize && duration * bps >= payloadBytes + HEADER_SIZE) {
      return { format, durationSec: duration, wavFileSize: wavSize };
    }
  }

  // If even the smallest format doesn't fit in one file, use voice format
  // with longer duration
  const voiceFormat = AUDIO_FORMATS['voice'];
  const bps = getBytesPerSecond(voiceFormat);
  const neededDuration = Math.ceil((payloadBytes + HEADER_SIZE) / bps);
  return { format: voiceFormat, durationSec: neededDuration, wavFileSize: neededDuration * bps + 44 };
}

// ── Utility: CRC32 (same as image-codec) ─────────────────────────────────

function crc32(buf: Buffer): number {
  let crc = 0xFFFFFFFF;
  for (let i = 0; i < buf.length; i++) {
    crc ^= buf[i];
    for (let j = 0; j < 8; j++) {
      if (crc & 1) {
        crc = (crc >>> 1) ^ 0xEDB88320;
      } else {
        crc = crc >>> 1;
      }
    }
  }
  return (crc ^ 0xFFFFFFFF) >>> 0;
}
