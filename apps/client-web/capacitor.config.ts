import type { CapacitorConfig } from '@capacitor/cli'

const config: CapacitorConfig = {
  appId: 'bypass.whitelist',
  appName: 'WhiteTransport',
  webDir: 'dist',
  // Embed mode: the Android project already exists at apps/android/whitelist-bypass-client
  // (with VpnService, gomobile mobile.aar, secrets). We do NOT use `cap add android`;
  // instead web assets are copied into that project's assets/public via `cap copy`,
  // pointed here so the CLI targets the existing module.
  android: {
    path: '../android/whitelist-bypass-client',
  },
}

export default config
