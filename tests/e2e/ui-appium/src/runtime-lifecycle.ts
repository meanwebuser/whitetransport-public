/** Returns whether the host runtime must be ready and populated before connect. */
export function requiresPreconnectRuntime(platform: string): boolean {
  return platform !== 'android';
}
