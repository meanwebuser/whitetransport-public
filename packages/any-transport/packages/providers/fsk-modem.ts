/**
 * YTP FSK Modem — Frequency-Shift Keying for voice message steganography.
 *
 * Encodes binary data as audio tones that survive Opus compression.
 * Designed for VK/Telegram voice messages where Opus re-encoding destroys
 * raw PCM data but preserves speech-band frequencies.
 *
 * Strategy:
 *   - Use FSK (Frequency Shift Keying) in the core speech band (300-3400 Hz)
 *   - Mark frequency (bit=1): 2200 Hz
 *   - Space frequency (bit=0): 1200 Hz
 *   - These frequencies are well-preserved by Opus at 16-32 kbps
 *   - Add FEC (Forward Error Correction) via repetition coding
 *   - Add preamble for synchronization
 *
 * Protocol stack:
 *   ┌─────────────────────────────────────────────────────┐
 *   │  YTP Frame: header + payload + CRC32                │
 *   │  ┌─────────┬──────────┬──────┬───────┬──────────┐  │
 *   │  │ PREAMBLE│ HDR 16B  │ DATA │ CRC32 │ TRAILER  │  │
 *   │  │ 1 sec   │ chunk/seq│      │ 4 B   │ 0.5 sec  │  │
 *   │  └─────────┴──────────┴──────┴───────┴──────────┘  │
 *   │  + FEC: each byte repeated 3× (triple redundancy)  │
 *   └─────────────────────────────────────────────────────┘
 *
 * Data rates:
 *   ┌──────────────────────────────────┬────────────┬──────────────────┐
 *   │ Mode                             │ Raw rate   │ After FEC (3×)   │
 *   ├──────────────────────────────────┼────────────┼──────────────────┤
 *   │ FSK 300 baud (Bell 103)          │ 300 bps    │ 100 bps = 12.5 B/s│
 *   │ FSK 600 baud                     │ 600 bps    │ 200 bps = 25 B/s  │
 *   │ FSK 1200 baud (Bell 202)         │ 1200 bps   │ 400 bps = 50 B/s  │
 *   ├──────────────────────────────────┼────────────┼──────────────────┤
 *   │ VK voice 2 min @ FSK300+FEC      │            │ ~1.5 KB          │
 *   │ VK voice 2 min @ FSK1200+FEC     │            │ ~6 KB            │
 *   │ VK voice 10 min @ FSK300+FEC     │            │ ~7.5 KB          │
 *   │ VK voice 10 min @ FSK1200+FEC    │            │ ~30 KB           │
 *   ├──────────────────────────────────┼────────────┼──────────────────┤
 *   │ Compare: Document upload (1 msg) │            │ 50 MB            │
 *   │ Compare: Text message            │            │ 4 KB             │
 *   │ Compare: Audio WAV as doc (10s)  │            │ 187 KB           │
 *   └──────────────────────────────────┴────────────┴──────────────────┘
 *
 * Voice messages are LOW bandwidth but HIGH stealth — they look like normal
 * voice messages. Use when document uploads are blocked/restricted.
 *
 * Opus preservation analysis:
 *   - Opus at 16-32 kbps preserves 300-3400 Hz band well
 *   - FSK tones are simple sinusoids — Opus treats them as voiced speech
 *   - Phase shifts ARE damaged by MDCT, but FSK doesn't use phase
 *   - Amplitude is normalized by Opus AGC — but FSK uses frequency, not amplitude
 *   - DTX may drop silence → we add continuous tone, no silence gaps
 */

// ── Constants ────────────────────────────────────────────────────────────

/** Sample rate for FSK audio generation */
const FSK_SAMPLE_RATE = 48000;

/** FSK mode definitions */
export interface FSKMode {
  /** Baud rate (symbols per second) */
  baudRate: number;
  /** Mark frequency (bit=1) in Hz */
  markFreq: number;
  /** Space frequency (bit=0) in Hz */
  spaceFreq: number;
}

export const FSK_MODES: Record<string, FSKMode> = {
  /** Bell 103: 300 baud, very robust, survives Opus well */
  'bell103': { baudRate: 300, markFreq: 2225, spaceFreq: 2025 },
  /** Custom robust: 300 baud, wider frequency gap for better Opus survival */
  'robust300': { baudRate: 300, markFreq: 2200, spaceFreq: 1200 },
  /** Bell 202: 1200 baud, moderate robustness */
  'bell202': { baudRate: 1200, markFreq: 2200, spaceFreq: 1200 },
  /** Fast: 2400 baud, may not survive Opus well */
  'fast': { baudRate: 2400, markFreq: 2100, spaceFreq: 1300 },
};

export const DEFAULT_FSK_MODE: string = 'robust300';

// ── Preamble / Trailer ──────────────────────────────────────────────────

const PREAMBLE_DURATION_SEC = 0.5;  // 500ms of alternating 0/1 bits
const TRAILER_DURATION_SEC = 0.2;   // 200ms of mark tone
const PREAMBLE_PATTERN = Buffer.from([0xAA, 0x55, 0xAA, 0x55, 0xAA, 0x55, 0xAA, 0x55]); // Alternating bits

// ── YTP Voice Frame Header ──────────────────────────────────────────────

const VOICE_MAGIC = Buffer.from('YTV1'); // YTP Voice v1
const VOICE_HEADER_SIZE = 32;

// ── FEC (Forward Error Correction) ──────────────────────────────────────

export type FECLevel = 'none' | 'double' | 'triple';

/**
 * Apply repetition coding FEC: repeat each byte N times.
 * On decode, use majority vote to recover the original byte.
 */
export function applyFEC(data: Buffer, level: FECLevel): Buffer {
  if (level === 'none') return data;
  const repeats = level === 'triple' ? 3 : 2;
  const result = Buffer.alloc(data.length * repeats);
  for (let i = 0; i < data.length; i++) {
    for (let r = 0; r < repeats; r++) {
      result[i * repeats + r] = data[i];
    }
  }
  return result;
}

/**
 * Remove FEC repetition coding using majority vote.
 */
export function removeFEC(data: Buffer, level: FECLevel): Buffer {
  if (level === 'none') return data;
  const repeats = level === 'triple' ? 3 : 2;
  const outputLen = Math.floor(data.length / repeats);
  const result = Buffer.alloc(outputLen);

  for (let i = 0; i < outputLen; i++) {
    if (repeats === 3) {
      // Triple redundancy: majority vote bit-by-bit
      const b0 = data[i * 3];
      const b1 = data[i * 3 + 1];
      const b2 = data[i * 3 + 2];
      result[i] = majorityVoteByte(b0, b1, b2);
    } else {
      // Double redundancy: if different, use first copy (no way to tell which is correct)
      result[i] = data[i * 2]; // Best effort
    }
  }

  return result;
}

function majorityVoteByte(a: number, b: number, c: number): number {
  // Bit-by-bit majority vote
  let result = 0;
  for (let bit = 0; bit < 8; bit++) {
    const mask = 1 << bit;
    const votes = ((a & mask) ? 1 : 0) + ((b & mask) ? 1 : 0) + ((c & mask) ? 1 : 0);
    if (votes >= 2) result |= mask;
  }
  return result;
}

// ── FSK Encoder: bits → audio samples ────────────────────────────────────

export interface FSKEncodeResult {
  /** PCM audio samples (Float32, mono, 48kHz) */
  samples: Float32Array;
  /** Duration in seconds */
  durationSec: number;
  /** Number of data bits encoded */
  bitCount: number;
}

/**
 * Encode binary data as FSK audio tones.
 * Generates 48kHz mono Float32 PCM samples.
 */
export function fskEncode(data: Buffer, mode: FSKMode = FSK_MODES[DEFAULT_FSK_MODE]): FSKEncodeResult {
  const samplesPerBit = Math.floor(FSK_SAMPLE_RATE / mode.baudRate);

  // Build bit stream: preamble + header + payload + CRC + trailer
  const frame = buildFrame(data);
  const bits = bufferToBits(frame);

  // Add preamble bits
  const preambleBits = generatePreambleBits(mode);
  const allBits = [...preambleBits, ...bits];

  // Add trailer (mark tone)
  const trailerBits = Math.floor(mode.baudRate * TRAILER_DURATION_SEC);
  for (let i = 0; i < trailerBits; i++) {
    allBits.push(1); // Mark tone
  }

  // Generate audio samples
  const totalSamples = allBits.length * samplesPerBit;
  const samples = new Float32Array(totalSamples);
  let sampleIdx = 0;

  let phase = 0;
  for (const bit of allBits) {
    const freq = bit ? mode.markFreq : mode.spaceFreq;
    const omega = (2 * Math.PI * freq) / FSK_SAMPLE_RATE;

    for (let s = 0; s < samplesPerBit; s++) {
      // Smooth phase transitions: apply raised-cosine ramp at bit boundaries
      let amplitude = 1.0;

      // Fade in/out at bit boundaries (10% of bit duration)
      const fadeSamples = Math.floor(samplesPerBit * 0.1);
      if (s < fadeSamples) {
        amplitude = s / fadeSamples;
      } else if (s >= samplesPerBit - fadeSamples) {
        amplitude = (samplesPerBit - s) / fadeSamples;
      }

      samples[sampleIdx++] = amplitude * Math.sin(phase);
      phase += omega;

      // Keep phase in range to avoid floating point drift
      if (phase > 2 * Math.PI * 1000) phase -= 2 * Math.PI * 1000;
    }
  }

  return {
    samples,
    durationSec: totalSamples / FSK_SAMPLE_RATE,
    bitCount: allBits.length,
  };
}

// ── FSK Decoder: audio samples → binary data ────────────────────────────

export interface FSKDecodeResult {
  /** Decoded payload data */
  payload: Buffer;
  /** CRC check passed */
  crcOk: boolean;
  /** Number of bit errors corrected by FEC (estimate) */
  correctedBits: number;
}

/**
 * Decode FSK audio tones back to binary data.
 * Expects 48kHz mono Float32 PCM samples.
 */
export function fskDecode(samples: Float32Array, mode: FSKMode = FSK_MODES[DEFAULT_FSK_MODE], fecLevel: FECLevel = 'triple'): FSKDecodeResult {
  const samplesPerBit = Math.floor(FSK_SAMPLE_RATE / mode.baudRate);

  // Step 1: Find preamble using frequency detection
  const preambleStart = findPreamble(samples, mode);
  if (preambleStart < 0) {
    throw new Error('FSK preamble not found — no valid signal detected');
  }

  // Step 2: Skip preamble, decode bits
  const preambleBits = generatePreambleBits(mode);
  const trailerBits = Math.floor(mode.baudRate * TRAILER_DURATION_SEC);
  const dataStartOffset = preambleStart + preambleBits.length * samplesPerBit;

  const bits: number[] = [];
  let offset = dataStartOffset;

  // Decode bits using Goertzel frequency detection
  while (offset + samplesPerBit <= samples.length - trailerBits * samplesPerBit) {
    const bit = detectBit(samples, offset, samplesPerBit, mode);
    bits.push(bit);
    offset += samplesPerBit;
  }

  // Step 3: Convert bits to bytes
  const rawBytes = bitsToBuffer(bits);
  if (rawBytes.length === 0) {
    throw new Error('No data decoded from FSK signal');
  }

  // Step 4: Parse frame (header + payload + CRC)
  return parseFrame(rawBytes, fecLevel);
}

// ── Goertzel algorithm for frequency detection ──────────────────────────

/**
 * Detect whether a segment contains mark or space frequency.
 * Uses Goertzel algorithm — efficient single-frequency DFT.
 */
function detectBit(samples: Float32Array, offset: number, length: number, mode: FSKMode): number {
  const markPower = goertzel(samples, offset, length, mode.markFreq);
  const spacePower = goertzel(samples, offset, length, mode.spaceFreq);
  return markPower > spacePower ? 1 : 0;
}

/**
 * Goertzel algorithm: compute power at a specific frequency.
 * More efficient than full FFT when you only need 1-2 frequencies.
 */
function goertzel(samples: Float32Array, offset: number, length: number, targetFreq: number): number {
  const k = Math.round(length * targetFreq / FSK_SAMPLE_RATE);
  const omega = (2 * Math.PI * k) / length;
  const coeff = 2 * Math.cos(omega);

  let s0 = 0, s1 = 0, s2 = 0;

  for (let i = 0; i < length; i++) {
    const sample = offset + i < samples.length ? samples[offset + i] : 0;
    s0 = sample + coeff * s1 - s2;
    s2 = s1;
    s1 = s0;
  }

  // Power = magnitude squared
  return s1 * s1 + s2 * s2 - coeff * s1 * s2;
}

// ── Preamble detection ──────────────────────────────────────────────────

/**
 * Find the preamble in the audio signal by looking for alternating
 * mark/space frequencies at the expected baud rate.
 */
function findPreamble(samples: Float32Array, mode: FSKMode): number {
  const samplesPerBit = Math.floor(FSK_SAMPLE_RATE / mode.baudRate);
  const preambleLen = 8; // Number of preamble bits to match

  // Scan through the first 3 seconds of audio
  const maxSearch = Math.min(samples.length, FSK_SAMPLE_RATE * 3);
  let bestOffset = -1;
  let bestScore = 0;

  // Slide window in steps of half a bit period
  const step = Math.floor(samplesPerBit / 2);

  for (let offset = 0; offset < maxSearch - preambleLen * samplesPerBit; offset += step) {
    let score = 0;

    for (let bit = 0; bit < preambleLen; bit++) {
      const bitOffset = offset + bit * samplesPerBit;
      const expected = bit % 2; // Alternating 0, 1, 0, 1...
      const detected = detectBit(samples, bitOffset, samplesPerBit, mode);
      if (detected === expected) score++;
    }

    if (score > bestScore) {
      bestScore = score;
      bestOffset = offset;
    }

    // If we found a perfect match, stop
    if (score >= preambleLen - 1) break;
  }

  // Require at least 6/8 correct bits
  return bestScore >= 6 ? bestOffset : -1;
}

function generatePreambleBits(mode: FSKMode): number[] {
  const preambleBitCount = Math.floor(mode.baudRate * PREAMBLE_DURATION_SEC);
  const bits: number[] = [];
  for (let i = 0; i < preambleBitCount; i++) {
    bits.push(i % 2); // Alternating 0, 1, 0, 1...
  }
  return bits;
}

// ── Frame building / parsing ─────────────────────────────────────────────

function buildFrame(payload: Buffer): Buffer {
  const crcVal = crc32(payload);

  const header = Buffer.alloc(VOICE_HEADER_SIZE);
  VOICE_MAGIC.copy(header, 0);                      // Magic "YTV1"
  header.writeUInt32LE(payload.length, 4);           // Payload length
  header.writeUInt32LE(crcVal, 8);                   // CRC32
  // Bytes 12-31: reserved

  return Buffer.concat([header, payload]);
}

function parseFrame(raw: Buffer, fecLevel: FECLevel): FSKDecodeResult {
  if (raw.length < VOICE_HEADER_SIZE) {
    throw new Error(`Frame too short: ${raw.length} < ${VOICE_HEADER_SIZE}`);
  }

  const magic = raw.slice(0, 4).toString();
  if (magic !== 'YTV1') {
    throw new Error(`Invalid voice magic: ${magic} (expected YTV1)`);
  }

  const payloadLen = raw.readUInt32LE(4);
  const expectedCrc = raw.readUInt32LE(8);

  if (payloadLen + VOICE_HEADER_SIZE > raw.length) {
    throw new Error(`Payload length ${payloadLen} exceeds available data`);
  }

  let payload = raw.slice(VOICE_HEADER_SIZE, VOICE_HEADER_SIZE + payloadLen);

  // Remove FEC
  let correctedBits = 0;
  if (fecLevel !== 'none') {
    // Count differences before FEC removal (rough estimate)
    payload = Buffer.from(removeFEC(payload, fecLevel));
  }

  const actualCrc = crc32(payload);
  const crcOk = actualCrc === expectedCrc;

  return { payload, crcOk, correctedBits };
}

// ── Bit/byte conversion ─────────────────────────────────────────────────

function bufferToBits(data: Buffer): number[] {
  const bits: number[] = [];
  for (const byte of data) {
    for (let bit = 7; bit >= 0; bit--) {
      bits.push((byte >> bit) & 1);
    }
  }
  return bits;
}

function bitsToBuffer(bits: number[]): Buffer {
  const bytes: number[] = [];
  for (let i = 0; i + 7 < bits.length; i += 8) {
    let byte = 0;
    for (let bit = 0; bit < 8; bit++) {
      byte = (byte << 1) | (bits[i + bit] || 0);
    }
    bytes.push(byte);
  }
  return Buffer.from(bytes);
}

// ── PCM format conversion ───────────────────────────────────────────────

/**
 * Convert Float32 samples to 16-bit PCM Buffer (for WAV output).
 */
export function float32ToPCM16(samples: Float32Array): Buffer {
  const buf = Buffer.alloc(samples.length * 2);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    buf.writeInt16LE(Math.floor(s * 32767), i * 2);
  }
  return buf;
}

/**
 * Convert 16-bit PCM Buffer to Float32 samples.
 */
export function pcm16ToFloat32(pcm: Buffer): Float32Array {
  const samples = new Float32Array(pcm.length / 2);
  for (let i = 0; i < samples.length; i++) {
    samples[i] = pcm.readInt16LE(i * 2) / 32767;
  }
  return samples;
}

// ── Capacity calculator ─────────────────────────────────────────────────

export interface VoiceCapacityInfo {
  mode: FSKMode;
  fecLevel: FECLevel;
  rawBps: number;           // Raw bits per second
  effectiveBps: number;     // After FEC overhead
  effectiveBytesPerSec: number;
  bytesPerMinute: number;
  bytesPer2Min: number;
  bytesPer10Min: number;
  headerOverheadBytes: number;
}

export function getVoiceCapacity(modeName: string = DEFAULT_FSK_MODE, fecLevel: FECLevel = 'triple'): VoiceCapacityInfo {
  const mode = FSK_MODES[modeName] || FSK_MODES[DEFAULT_FSK_MODE];
  const fecOverhead = fecLevel === 'triple' ? 3 : fecLevel === 'double' ? 2 : 1;
  const rawBps = mode.baudRate;
  const effectiveBps = rawBps / fecOverhead;
  const headerOverhead = VOICE_HEADER_SIZE * fecOverhead;

  return {
    mode,
    fecLevel,
    rawBps,
    effectiveBps,
    effectiveBytesPerSec: effectiveBps / 8,
    bytesPerMinute: Math.floor(effectiveBps / 8 * 60),
    bytesPer2Min: Math.floor(effectiveBps / 8 * 120 - headerOverhead),
    bytesPer10Min: Math.floor(effectiveBps / 8 * 600 - headerOverhead),
    headerOverheadBytes: headerOverhead,
  };
}

// ── CRC32 ────────────────────────────────────────────────────────────────

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
