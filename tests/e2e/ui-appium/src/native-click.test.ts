import assert from 'node:assert/strict';
import test from 'node:test';

import { clickNativeElement, type NativeClickableElement } from './native-click.ts';

test('native click waits for display and enabled state without waitForClickable', async () => {
  const calls: string[] = [];
  const element: NativeClickableElement = {
    async waitForDisplayed(): Promise<void> { calls.push('displayed'); },
    async waitForEnabled(): Promise<void> { calls.push('enabled'); },
    async click(): Promise<void> { calls.push('click'); },
  };

  await clickNativeElement(element, 1234);

  assert.deepEqual(calls, ['displayed', 'enabled', 'click']);
  assert.equal('waitForClickable' in element, false);
});
