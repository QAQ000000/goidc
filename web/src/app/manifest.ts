import type { MetadataRoute } from "next";

export default function manifest(): MetadataRoute.Manifest {
  return {
    name: "云服务账户",
    short_name: "账户",
    description: "登录查看服务和业务通知",
    start_url: "/console/account",
    display: "standalone",
    background_color: "#ffffff",
    theme_color: "#ffffff",
    icons: [
      { src: "/assets/notification-icon-192.png", sizes: "192x192", type: "image/png" },
      { src: "/assets/notification-icon-512.png", sizes: "512x512", type: "image/png" },
    ],
  };
}
