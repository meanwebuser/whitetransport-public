import assert from 'node:assert/strict';
import test from 'node:test';

import { requiresPreconnectRuntime } from './runtime-lifecycle.ts';

test('Android starts runtime and discovery from the connect action', () => {
  assert.equal(requiresPreconnectRuntime('android'), false);
});

test('desktop requires a ready runtime and discovered node before connect', () => {
  assert.equal(requiresPreconnectRuntime('desktop'), true);
});
