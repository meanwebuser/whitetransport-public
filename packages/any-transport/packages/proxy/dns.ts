/**
 * YTP DNS Layer — dedicated DNS resolution over Y Transport.
 *
 * Instead of tunnelling DNS through a raw stream (expensive),
 * we send a typed DNS operation and cache the result.
 */

export interface DnsQuery {
  name: string;
  qtype: 'A' | 'AAAA' | 'TXT' | 'MX';
}

export interface DnsAnswer {
  name: string;
  qtype: string;
  answers: string[];
  ttl: number;
  resolvedAt: number;
}

export class DnsCache {
  private cache: Map<string, DnsAnswer> = new Map();

  get(name: string, qtype: string): DnsAnswer | null {
    const key = `${qtype}:${name}`;
    const entry = this.cache.get(key);
    if (!entry) return null;

    // Check TTL
    if (Date.now() - entry.resolvedAt > entry.ttl * 1000) {
      this.cache.delete(key);
      return null;
    }

    return entry;
  }

  set(answer: DnsAnswer): void {
    const key = `${answer.qtype}:${answer.name}`;
    this.cache.set(key, answer);
  }

  clear(): void {
    this.cache.clear();
  }

  get size(): number {
    return this.cache.size;
  }
}
