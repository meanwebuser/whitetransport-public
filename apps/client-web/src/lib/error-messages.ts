/**
 * Maps runtime and smoke-test failures to concise operator-facing messages.
 */

const patterns: Array<[RegExp, string]> = [
  [/no nodes discovered/i, 'No runtime nodes are currently available on the discovery carriers.'],
  [/runtime connect returned http 409/i, 'The selected node is busy. Wait for it to advertise again and retry.'],
  [/runtime connect returned http 5\d\d/i, 'The daemon accepted the request but the runtime failed it server-side.'],
  [/socks/i, 'The SOCKS listener did not come up cleanly. Check daemon logs and local port usage.'],
  [/health/i, 'The daemon health endpoint did not respond in time.'],
  [/method not allowed/i, 'The desktop bridge is older than the UI expects. Rebuild the desktop app.'],
];

export function toHumanErrorMessage(input: string | null | undefined): string {
  if (!input) {
    return 'Unknown runtime error.';
  }
  for (const [pattern, message] of patterns) {
    if (pattern.test(input)) {
      return message;
    }
  }
  return input;
}
