"use client";
// 文档级错误边界：它替换根布局，因此不依赖 globals.css 与站点数据，只用内联样式。
export default function GlobalError({ error, reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return (
    <html lang="zh-CN">
      <body style={{ margin: 0, minHeight: "100vh", display: "grid", placeItems: "center", padding: 24, fontFamily: "system-ui, -apple-system, \"Segoe UI\", sans-serif", background: "#ffffff", color: "#18181b" }}>
        <main style={{ maxWidth: 520, width: "100%", textAlign: "center" }}>
          <h1 style={{ fontSize: 22, margin: "0 0 12px" }}>站点暂时无法加载</h1>
          <p style={{ margin: "0 0 8px", color: "#52525b", lineHeight: 1.7 }}>
            页面在加载基础环境时遇到错误。可以重试；如果反复出现，请联系站点支持。
          </p>
          <p role="alert" style={{ margin: "0 0 20px", fontSize: 13, color: "#b91c1c", wordBreak: "break-all" }}>
            {error.message || "未知错误"}
            {error.digest ? `（错误编号：${error.digest}）` : ""}
          </p>
          <button type="button" onClick={reset} style={{ padding: "9px 18px", border: "1px solid #d4d4d8", borderRadius: 6, background: "#18181b", color: "#ffffff", fontSize: 14, cursor: "pointer" }}>
            重新加载
          </button>
        </main>
      </body>
    </html>
  );
}
