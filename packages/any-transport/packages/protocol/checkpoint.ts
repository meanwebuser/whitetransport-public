/**
 * YTP Checkpoint — logical garbage-collection marker.
 *
 * After a checkpoint is acknowledged, all earlier envelopes can be
 * safely ignored (even if they remain physically on the provider).
 */

export interface Checkpoint {
  epoch: number;
  receivedUpTo: number;
  stateHash: string;
}

export function checkpointToOp(cp: Checkpoint) {
  return {
    op: 'checkpoint' as const,
    id: crypto.randomUUID(),
    epoch: cp.epoch,
    receivedUpTo: cp.receivedUpTo,
    stateHash: cp.stateHash,
  };
}
