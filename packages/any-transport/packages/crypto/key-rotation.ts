/**
 * YTP Key Rotation — automatic session key rotation policy.
 *
 * Rotation triggers:
 *   - Every N bundles sent
 *   - Every T hours
 *   - Provider change
 *   - Manual user action
 */

export interface RotationPolicy {
  maxBundles: number;     // rotate after this many bundles
  maxDurationMs: number;  // rotate after this much time
  onProviderChange: boolean;
}

export const DEFAULT_ROTATION_POLICY: RotationPolicy = {
  maxBundles: 10_000,
  maxDurationMs: 24 * 3600 * 1000, // 24 hours
  onProviderChange: true,
};

export interface RotationState {
  epochId: number;
  bundlesSinceRotation: number;
  lastRotationAt: number; // epoch-ms
}

export function createRotationState(): RotationState {
  return {
    epochId: 1,
    bundlesSinceRotation: 0,
    lastRotationAt: Date.now(),
  };
}

export function shouldRotate(state: RotationState, policy: RotationPolicy = DEFAULT_ROTATION_POLICY): boolean {
  if (state.bundlesSinceRotation >= policy.maxBundles) return true;
  if (Date.now() - state.lastRotationAt >= policy.maxDurationMs) return true;
  return false;
}

export function advanceEpoch(state: RotationState): RotationState {
  return {
    epochId: state.epochId + 1,
    bundlesSinceRotation: 0,
    lastRotationAt: Date.now(),
  };
}
