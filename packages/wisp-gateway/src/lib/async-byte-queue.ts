export class AsyncByteQueue {
  readonly max_size: number;
  private items: Buffer[] = [];
  private waiters: Array<(value: Buffer | null) => void> = [];
  private isClosed = false;

  constructor(maxSize = 128) {
    this.max_size = maxSize;
  }

  get size(): number {
    return this.items.length;
  }

  put(data: Buffer | Uint8Array): void {
    if (this.isClosed) return;
    const value = Buffer.isBuffer(data) ? data : Buffer.from(data);
    const waiter = this.waiters.shift();
    if (waiter) {
      waiter(value);
      return;
    }
    this.items.push(value);
  }

  async get(): Promise<Buffer | null> {
    const item = this.items.shift();
    if (item) return item;
    if (this.isClosed) return null;
    return await new Promise<Buffer | null>((resolve) => this.waiters.push(resolve));
  }

  close(): void {
    if (this.isClosed) return;
    this.isClosed = true;
    for (const waiter of this.waiters.splice(0)) waiter(null);
  }
}
