import assert from 'node:assert/strict';
import { test } from 'node:test';
import { NodeRuntimeLauncher } from '../dist/node.js';

test('NodeRuntimeLauncher tracks a successful child process exit', async () => {
  const launcher = new NodeRuntimeLauncher();
  const runtime = await launcher.launch({
    executable: process.execPath,
    args: ['-e', 'process.exit(0)'],
  });

  const exit = await runtime.exited;

  assert.equal(typeof runtime.pid, 'number');
  assert.equal(exit.code, 0);
});

test('NodeRuntimeLauncher can stop a long-running child process', async () => {
  const launcher = new NodeRuntimeLauncher();
  const runtime = await launcher.launch({
    executable: process.execPath,
    args: ['-e', 'setInterval(() => {}, 1000)'],
  });

  await runtime.stop();
  const exit = await runtime.exited;

  assert.equal(exit.code, null);
  assert.equal(exit.signal, 'SIGTERM');
});
