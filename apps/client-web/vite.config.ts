import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  // Relative asset paths so the one build works under every WebView scheme:
  // https://localhost (Android), capacitor://localhost (iOS), and file://
  // (Electron desktop, where absolute /assets would resolve to the FS root).
  base: './',
  plugins: [react()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  // Capacitor (Step B) serves the static build from webDir = dist.
  build: { outDir: 'dist' },
})
