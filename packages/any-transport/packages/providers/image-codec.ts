/**
 * YTP ImageCodec — Encode arbitrary binary data into images and decode back.
 *
 * Strategy: Pack raw bytes into pixel RGB channels (3 bytes per pixel).
 * A 256x256 image carries ~192KB of raw data.
 * A 1024x1024 image carries ~3MB of raw data.
 *
 * Two transport modes:
 *
 * 1. DOCUMENT MODE (VK docs.upload, OK file upload):
 *    - Uploads raw PNG as a document attachment
 *    - VK/OK do NOT re-encode documents
 *    - Full data integrity preserved
 *    - Use this mode for maximum throughput
 *
 * 2. PHOTO MODE (VK photos, OK photos):
 *    - Uploads as photo attachment
 *    - VK/OK re-encode to JPEG → pixel data destroyed
 *    - Use JPEG-resistant steganography (DCT-domain)
 *    - Lower capacity but works as photo attachment
 *    - NOTE: Currently NOT recommended — use document mode instead
 *
 * Header format (first 16 pixels = 48 bytes):
 *   Bytes 0-3:  Magic "YTP1"
 *   Bytes 4-7:  Payload length (uint32 LE)
 *   Bytes 8-11: Chunk index (uint32 LE)
 *   Bytes 12-15: Total chunks (uint32 LE)
 *   Bytes 16-19: CRC32 of payload (uint32 LE)
 *   Bytes 20-47: Reserved (zeros)
 *
 * VK documents support up to 50MB, OK files up to 100MB.
 * Default: 256x256 images for low latency (~192KB payload each).
 * Max: 1024x1024 for high throughput (~3MB per chunk).
 */

import { createHash, randomBytes } from 'crypto';
import * as fs from 'fs';
import * as path from 'path';
import { deflateSync, inflateSync } from 'zlib';

// ── Constants ────────────────────────────────────────────────────────────

const MAGIC = Buffer.from('YTP1');
const HEADER_SIZE = 48; // 16 pixels × 3 bytes
const DEFAULT_IMAGE_WIDTH = 256;
const DEFAULT_IMAGE_HEIGHT = 256;

// ── Encode: data → raw RGBA pixels ───────────────────────────────────────

export interface EncodedImage {
  width: number;
  height: number;
  data: Buffer;  // RGBA pixel data (width × height × 4)
  payloadSize: number;
  chunkIndex: number;
  totalChunks: number;
}

export function encodeToPixels(
  payload: Buffer,
  chunkIndex: number = 0,
  totalChunks: number = 1,
  width: number = DEFAULT_IMAGE_WIDTH,
  height: number = DEFAULT_IMAGE_HEIGHT,
): EncodedImage {
  const pixels = width * height;
  const maxPayload = pixels * 3 - HEADER_SIZE;
  const payloadLen = payload.length;

  if (payloadLen > maxPayload) {
    throw new Error(`Payload too large: ${payloadLen} > ${maxPayload} (for ${width}x${height})`);
  }

  // Build header
  const header = Buffer.alloc(HEADER_SIZE);
  MAGIC.copy(header, 0);                         // Magic
  header.writeUInt32LE(payloadLen, 4);            // Payload length
  header.writeUInt32LE(chunkIndex, 8);            // Chunk index
  header.writeUInt32LE(totalChunks, 12);          // Total chunks
  // CRC32 placeholder — we'll use a simple hash
  const crcVal = crc32(payload);
  header.writeUInt32LE(crcVal, 16);              // CRC32

  // Combine header + payload
  const dataBuffer = Buffer.concat([header, payload]);

  // Pad to fill all pixels
  const totalBytes = pixels * 3;
  const padded = Buffer.alloc(totalBytes);
  dataBuffer.copy(padded, 0);
  // Fill remaining with random noise (looks less suspicious)
  randomBytes(totalBytes - dataBuffer.length).copy(padded, dataBuffer.length);

  // Convert to RGBA (add alpha channel = 255)
  const rgba = Buffer.alloc(pixels * 4);
  for (let i = 0; i < pixels; i++) {
    rgba[i * 4]     = padded[i * 3];     // R
    rgba[i * 4 + 1] = padded[i * 3 + 1]; // G
    rgba[i * 4 + 2] = padded[i * 3 + 2]; // B
    rgba[i * 4 + 3] = 255;               // A
  }

  return {
    width,
    height,
    data: rgba,
    payloadSize: payloadLen,
    chunkIndex,
    totalChunks,
  };
}

// ── Decode: raw RGBA pixels → data ───────────────────────────────────────

export interface DecodedImage {
  payload: Buffer;
  chunkIndex: number;
  totalChunks: number;
  crcOk: boolean;
}

export function decodeFromPixels(rgbaData: Buffer, width: number, height: number): DecodedImage {
  const pixelCount = width * height;

  // Extract RGB from RGBA
  const rgb = Buffer.alloc(pixelCount * 3);
  for (let i = 0; i < pixelCount; i++) {
    rgb[i * 3]     = rgbaData[i * 4];     // R
    rgb[i * 3 + 1] = rgbaData[i * 4 + 1]; // G
    rgb[i * 3 + 2] = rgbaData[i * 4 + 2]; // B
  }

  // Parse header
  const magic = rgb.slice(0, 4).toString();
  if (magic !== 'YTP1') {
    throw new Error(`Invalid magic: ${magic} (expected YTP1)`);
  }

  const payloadLen = rgb.readUInt32LE(4);
  const chunkIndex = rgb.readUInt32LE(8);
  const totalChunks = rgb.readUInt32LE(12);
  const expectedCrc = rgb.readUInt32LE(16);

  // Extract payload
  if (payloadLen > pixelCount * 3 - HEADER_SIZE) {
    throw new Error(`Invalid payload length: ${payloadLen}`);
  }

  const payload = rgb.slice(HEADER_SIZE, HEADER_SIZE + payloadLen);

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

// ── Split data into multiple image chunks ─────────────────────────────────

export function splitIntoChunks(data: Buffer, width: number = DEFAULT_IMAGE_WIDTH, height: number = DEFAULT_IMAGE_HEIGHT): EncodedImage[] {
  const maxPayload = width * height * 3 - HEADER_SIZE;
  const totalChunks = Math.ceil(data.length / maxPayload);

  const chunks: EncodedImage[] = [];
  for (let i = 0; i < totalChunks; i++) {
    const start = i * maxPayload;
    const end = Math.min(start + maxPayload, data.length);
    const chunk = data.slice(start, end);

    chunks.push(encodeToPixels(chunk, i, totalChunks, width, height));
  }

  return chunks;
}

// ── Reassemble chunks ────────────────────────────────────────────────────

export function reassembleChunks(decoded: DecodedImage[]): Buffer {
  // Sort by chunk index
  const sorted = [...decoded].sort((a, b) => a.chunkIndex - b.chunkIndex);

  // Verify we have all chunks
  const totalChunks = sorted[0]?.totalChunks || 0;
  if (sorted.length !== totalChunks) {
    throw new Error(`Missing chunks: have ${sorted.length}, expected ${totalChunks}`);
  }

  // Verify CRCs
  for (const chunk of sorted) {
    if (!chunk.crcOk) {
      throw new Error(`CRC mismatch in chunk ${chunk.chunkIndex}`);
    }
  }

  // Concatenate payloads
  return Buffer.concat(sorted.map(c => c.payload));
}

// ── PNG encode (minimal, no external deps) ────────────────────────────────
// Minimal PNG encoder: creates a valid PNG file from raw RGBA pixel data.
// PNG is lossless and preserves all pixel data — perfect for document uploads.

export function encodeToPNG(image: EncodedImage): Buffer {
  const { width, height, data } = image;

  // Build raw image data (with filter byte 0 = None per row)
  const rawRowSize = width * 4; // RGBA
  const rawData = Buffer.alloc((rawRowSize + 1) * height);
  for (let y = 0; y < height; y++) {
    rawData[y * (rawRowSize + 1)] = 0; // Filter: None
    data.copy(rawData, y * (rawRowSize + 1) + 1, y * rawRowSize, (y + 1) * rawRowSize);
  }

  // Compress with zlib (deflate)
  const compressed = deflateSync(rawData);

  // Build PNG file
  const chunks: Buffer[] = [];

  // PNG signature
  chunks.push(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]));

  // IHDR chunk
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8;  // bit depth
  ihdr[9] = 6;  // color type: RGBA
  ihdr[10] = 0; // compression
  ihdr[11] = 0; // filter
  ihdr[12] = 0; // interlace
  chunks.push(makePNGChunk('IHDR', ihdr));

  // IDAT chunk
  chunks.push(makePNGChunk('IDAT', compressed));

  // IEND chunk
  chunks.push(makePNGChunk('IEND', Buffer.alloc(0)));

  return Buffer.concat(chunks);
}

// ── PNG decode (minimal) ──────────────────────────────────────────────────

export function decodeFromPNG(pngBuffer: Buffer): { width: number; height: number; rgba: Buffer } {
  // Verify PNG signature
  const sig = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  if (!pngBuffer.slice(0, 8).equals(sig)) {
    throw new Error('Not a valid PNG file');
  }

  let width = 0, height = 0;
  let idatData: Buffer[] = [];
  let offset = 8;

  while (offset < pngBuffer.length) {
    const chunkLen = pngBuffer.readUInt32BE(offset);
    const chunkType = pngBuffer.slice(offset + 4, offset + 8).toString();

    if (chunkType === 'IHDR') {
      width = pngBuffer.readUInt32BE(offset + 8);
      height = pngBuffer.readUInt32BE(offset + 12);
    } else if (chunkType === 'IDAT') {
      idatData.push(pngBuffer.slice(offset + 8, offset + 8 + chunkLen));
    } else if (chunkType === 'IEND') {
      break;
    }

    offset += 12 + chunkLen; // 4(len) + 4(type) + data + 4(crc)
  }

  if (width === 0 || height === 0) {
    throw new Error('Invalid PNG: missing IHDR');
  }

  // Decompress IDAT
  const compressed = Buffer.concat(idatData);
  const raw = inflateSync(compressed);

  // Remove filter bytes (assume filter=0 None for simplicity)
  const rowSize = width * 4 + 1; // RGBA + filter byte
  const rgba = Buffer.alloc(width * height * 4);
  for (let y = 0; y < height; y++) {
    const filterByte = raw[y * rowSize];
    if (filterByte !== 0) {
      // For simplicity, only handle filter=0 (None)
      // TODO: implement filter reconstruction for other filter types
      console.warn(`PNG filter type ${filterByte} not fully supported, data may be corrupted`);
    }
    raw.copy(rgba, y * width * 4, y * rowSize + 1, (y + 1) * rowSize);
  }

  return { width, height, rgba };
}

// ── Full encode pipeline: binary data → PNG buffer ────────────────────────

export function encodeDataToPNG(data: Buffer, chunkIndex: number = 0, totalChunks: number = 1, width?: number, height?: number): Buffer {
  const img = encodeToPixels(data, chunkIndex, totalChunks, width, height);
  return encodeToPNG(img);
}

// ── Full decode pipeline: PNG buffer → binary data ────────────────────────

export function decodeDataFromPNG(pngBuffer: Buffer): DecodedImage {
  const { width, height, rgba } = decodeFromPNG(pngBuffer);
  return decodeFromPixels(rgba, width, height);
}

// ── PPM encode (legacy, for debugging) ────────────────────────────────────

export function encodeToPPM(image: EncodedImage): Buffer {
  // PPM format: P6\nwidth height\n255\n<raw RGB>
  const header = Buffer.from(`P6\n${image.width} ${image.height}\n255\n`);

  // Extract RGB from RGBA
  const rgb = Buffer.alloc(image.width * image.height * 3);
  for (let i = 0; i < image.width * image.height; i++) {
    rgb[i * 3]     = image.data[i * 4];     // R
    rgb[i * 3 + 1] = image.data[i * 4 + 1]; // G
    rgb[i * 3 + 2] = image.data[i * 4 + 2]; // B
  }

  return Buffer.concat([header, rgb]);
}

// ── Utility: simple CRC32 ────────────────────────────────────────────────

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

// ── PNG chunk builder ─────────────────────────────────────────────────────

function makePNGChunk(type: string, data: Buffer): Buffer {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length, 0);

  const typeBuffer = Buffer.from(type, 'ascii');
  const crcData = Buffer.concat([typeBuffer, data]);

  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32png(crcData), 0);

  return Buffer.concat([len, typeBuffer, data, crc]);
}

// CRC32 for PNG (same algorithm but signed for PNG spec compliance)
function crc32png(buf: Buffer): number {
  // PNG uses the same CRC32 as above
  return crc32(buf);
}

// ── Stats ────────────────────────────────────────────────────────────────

export function getImageStats(width: number = DEFAULT_IMAGE_WIDTH, height: number = DEFAULT_IMAGE_HEIGHT) {
  const maxPayload = width * height * 3 - HEADER_SIZE;
  return {
    imageWidth: width,
    imageHeight: height,
    maxPayloadPerImage: maxPayload,
    maxPayloadKB: Math.floor(maxPayload / 1024),
    headerSize: HEADER_SIZE,
    magic: MAGIC.toString(),
  };
}

/**
 * Calculate optimal image dimensions for a given payload size.
 * Tries to find the smallest square image that can hold the data.
 */
export function optimalImageSize(payloadBytes: number): { width: number; height: number } {
  const neededPixels = Math.ceil((payloadBytes + HEADER_SIZE) / 3);
  const side = Math.ceil(Math.sqrt(neededPixels));

  // Round up to nearest power of 2 for cleaner PNG compression
  const p2 = Math.pow(2, Math.ceil(Math.log2(side)));

  // Don't exceed 4096 (practical limit for most APIs)
  const finalSide = Math.min(p2, 4096);

  return { width: finalSide, height: finalSide };
}
