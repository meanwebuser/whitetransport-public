export function androidTextMatches(labels: readonly string[]): string {
  const escaped = labels.map((label) => label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
  return `^(?:${escaped.join('|')})$`;
}
