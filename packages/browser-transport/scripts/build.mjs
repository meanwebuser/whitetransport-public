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
