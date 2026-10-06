import type { NextConfig } from "next";
const config: NextConfig = {
  experimental: { proxyClientMaxBodySize: 52 * 1024 * 1024 },
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
