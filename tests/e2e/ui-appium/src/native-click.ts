export interface NativeClickableElement {
  waitForDisplayed(options?: { timeout?: number }): Promise<void>;
  waitForEnabled(options?: { timeout?: number }): Promise<void>;
  click(): Promise<void>;
}

/** Waits using commands supported by native UiAutomator2 before clicking. */
export async function clickNativeElement(element: NativeClickableElement, timeout = 30_000): Promise<void> {
  await element.waitForDisplayed({ timeout });
  await element.waitForEnabled({ timeout });
  await element.click();
}
