import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../../../..');

export function androidCapabilities(): Record<string, unknown> {
  const app = process.env.WT_E2E_APK
    ? path.resolve(process.env.WT_E2E_APK)
    : path.resolve(repoRoot, 'artifacts/android/WhiteTransport-dev-release.apk');
  const udid = process.env.WT_E2E_ANDROID_UDID
    ?? process.env.WT_E2E_ANDROID_SERIAL
    ?? process.env.ANDROID_SERIAL;

  const capabilities: Record<string, unknown> = {
    platformName: 'Android',
    'appium:automationName': 'UiAutomator2',
    'appium:deviceName': process.env.WT_E2E_ANDROID_DEVICE ?? udid ?? 'Android',
    'appium:app': app,
    'appium:appPackage': process.env.WT_E2E_ANDROID_PACKAGE ?? 'bypass.whitelist',
    'appium:appActivity': process.env.WT_E2E_ANDROID_ACTIVITY ?? '.CapacitorMainActivity',
    'appium:appWaitActivity': process.env.WT_E2E_ANDROID_WAIT_ACTIVITY ?? '.CapacitorMainActivity',
    // Real-device lanes commonly rebuild without bumping versionCode. Always
    // replace the installed package so Appium cannot exercise stale assets.
    'appium:enforceAppInstall': true,
    'appium:forceAppLaunch': true,
    'appium:shouldTerminateApp': true,
    'appium:autoGrantPermissions': true,
    'appium:newCommandTimeout': 180,
  };

  if (udid) {
    capabilities['appium:udid'] = udid;
  }

  return capabilities;
}
