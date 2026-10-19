import type { NextConfig } from "next";
import path from "node:path";
// Only the isolated v2 builder sets this flag. Normal/v1 builds retain their
// existing output; tracing never expands beyond this fixed checkout root.
const runtimeBuild = process.env.ZJMFGO_RUNTIME_BUILD === "1";
const config: NextConfig = {
  ...(runtimeBuild ? { output: "standalone", outputFileTracingRoot: path.resolve(process.cwd(), "..") } : {}),
  experimental: { proxyClientMaxBodySize: 52 * 1024 * 1024 },
  async headers() {
    return [{ source: "/notification-sw.js", headers: [
      { key: "Content-Type", value: "application/javascript; charset=utf-8" },
      { key: "Cache-Control", value: "no-cache, no-store, must-revalidate" },
      { key: "X-Content-Type-Options", value: "nosniff" },
      { key: "Content-Security-Policy", value: "default-src 'self'; script-src 'self'" },
    ] }];
  },
  async rewrites() {
    return [
      {
        source: "/api/:path*",
        destination: `${process.env.API_INTERNAL_URL ?? "http://127.0.0.1:8088"}/api/:path*`,
      },
    ];
  },
};
export default config;
