import esbuild from 'esbuild';

await esbuild.build({
  entryPoints: ['src/global-install.js'],
  outfile: 'dist/whitetransport-wb.js',
  bundle: true,
  format: 'iife',
  target: ['safari16'],
  sourcemap: true,
  minify: false,
  define: {
    'process.env.NODE_ENV': '"production"',
  },
});

await esbuild.build({
  entryPoints: ['src/index.js'],
  outfile: 'dist/whitetransport-wb.esm.js',
  bundle: true,
  format: 'esm',
  target: ['safari16'],
  sourcemap: true,
  minify: false,
  define: {
    'process.env.NODE_ENV': '"production"',
  },
});

/* Worker bundle: no livekit-client (SharedWorker + LiveKit is a risk), only
 * the wisp framing adapter. Loaded into the baremux worker via importScripts
 * from the /baremux/worker.js wrapper served by mobile-browser. */
await esbuild.build({
  entryPoints: ['src/worker-install.js'],
  outfile: 'dist/whitetransport-wb-worker.js',
  bundle: true,
  format: 'iife',
  target: ['safari16'],
  sourcemap: true,
  minify: false,
  define: {
    'process.env.NODE_ENV': '"production"',
  },
});
