import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

test('guarded real video-conference credential smoke test', { skip: process.env.WT_VIDEO_SMOKE !== '1' }, () => {
  const envText = readFileSync(new URL('../../../.env', import.meta.url), 'utf8');
  const credentialLines = envText
    .split(/\r?\n/)
    .slice(26, 39)
    .filter((line) => /^[A-Z0-9_]+=.+/.test(line));

  assert.ok(credentialLines.length > 0, 'expected opt-in smoke credentials in .env lines 27-39');
  for (const line of credentialLines) {
    const [name, value = ''] = line.split('=', 2);
    assert.ok(name.length > 0);
    assert.ok(value.length > 0, `${name} should be populated`);
  }
});
