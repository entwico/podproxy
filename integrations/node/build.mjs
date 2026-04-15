import { build } from "esbuild";

await build({
  entryPoints: ["src/main.ts"],
  bundle: true,
  platform: "node",
  format: "esm",
  outfile: "dist/proxy.mjs",
  treeShaking: true,
  banner: {
    js: [
      "// podproxy - Node.js proxy integration",
      "// https://github.com/entwico/podproxy",
      "// MIT License",
      "//",
      "// This script patches dns and net modules to route TCP connections",
      "// through podproxy's SOCKS5 proxy for matched hostnames.",
      "import { createRequire } from 'module'; const require = createRequire(import.meta.url);",
    ].join("\n"),
  },
});
