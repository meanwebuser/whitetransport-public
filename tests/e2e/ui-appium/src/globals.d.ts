declare function describe(name: string, fn: () => void): void;
declare function it(name: string, fn: (this: { skip(): void; timeout(ms: number): void }) => Promise<void> | void): void;
