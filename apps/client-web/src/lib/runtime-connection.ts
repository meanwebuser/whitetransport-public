export type RuntimeConnectTarget =
  | { readonly kind: 'server'; readonly serverId: string }
  | { readonly kind: 'runtime' }
  | { readonly kind: 'unavailable' };

interface RuntimeConnectCandidates {
  readonly selectedServerId?: string;
  readonly preferredServerId?: string;
  readonly onlineServerId?: string;
  readonly knownServerIds: readonly string[];
  readonly capacitorHost: boolean;
}

/** Resolves whether UI or the native runtime selects the node for connection. */
export function resolveRuntimeConnectTarget(candidates: RuntimeConnectCandidates): RuntimeConnectTarget {
  const knownServerIds = new Set(candidates.knownServerIds);
  const serverId = [candidates.selectedServerId, candidates.preferredServerId, candidates.onlineServerId]
    .find((candidate): candidate is string => Boolean(candidate && knownServerIds.has(candidate)));
  if (serverId) return { kind: 'server', serverId };
  if (candidates.capacitorHost) return { kind: 'runtime' };
  return { kind: 'unavailable' };
}
