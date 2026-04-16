import { build } from "esbuild";

const banner = [
  "// podproxy - Node.js proxy integration",
  "// https://github.com/entwico/podproxy",
  "// MIT License",
  "//",
  "// This script patches dns and net modules to route TCP connections",
  "// through podproxy's SOCKS5 proxy for matched hostnames.",
].join("\n");

await Promise.all([
  build({
    entryPoints: ["src/main-esm.ts"],
    bundle: true,
    platform: "node",
    format: "esm",
    outfile: "dist/proxy.mjs",
    treeShaking: true,
    banner: {
      js: [
        banner,
        "import { createRequire } from 'module'; const require = createRequire(import.meta.url);",
      ].join("\n"),
    },
  }),
  build({
    entryPoints: ["src/main-cjs.ts"],
    bundle: true,
    platform: "node",
    format: "cjs",
    outfile: "dist/proxy.cjs",
    treeShaking: true,
    banner: { js: banner },
  }),
]);
