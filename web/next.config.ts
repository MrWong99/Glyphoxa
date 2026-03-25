import type { NextConfig } from "next";

const apiBackendUrl =
  process.env.API_BACKEND_URL || "http://localhost:8090";

const nextConfig: NextConfig = {
  output: "standalone",
  async rewrites() {
    return [
      {
        source: "/api/:path*",
        destination: `${apiBackendUrl}/api/:path*`,
      },
    ];
  },
};

export default nextConfig;
