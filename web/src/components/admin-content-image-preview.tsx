import Image from "next/image";

// Preview only canonical uploaded image references. Never render editor HTML or
// rewrite the article body: published content must retain its public image URLs.
export function adminContentImagePreviews(html: string) {
  const images = new Map<string, string>();
  for (const tag of html.matchAll(/<img\b(?:[^"'<>]|"[^"]*"|'[^']*')*>/gi)) {
    for (const attribute of tag[0].slice(4, -1).matchAll(/(?:^|\s)([^\s=/>]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+))/g)) {
      if (attribute[1].toLowerCase() !== "src") continue;
      const source = attribute[2] ?? attribute[3] ?? attribute[4];
      const id = /^\/api\/v1\/content-images\/([1-9]\d{0,18})$/.exec(source)?.[1];
      if (id) images.set(source, `/api/v1/admin/content-images/${id}`);
      break;
    }
  }
  return [...images].map(([source, preview]) => ({ source, preview }));
}

export function AdminContentImagePreview({ html }: { html: string }) {
  const images = adminContentImagePreviews(html);
  if (!images.length) return null;
  return <section aria-label="正文图片预览" className="space-y-3 rounded-md border p-4">
    <h3 className="text-sm font-semibold">正文图片预览</h3>
    <div className="grid gap-3 sm:grid-cols-2">{images.map(({ source, preview }, index) => <Image key={source} src={preview} alt={`正文图片 ${index + 1}`} width={480} height={320} unoptimized className="h-48 w-full rounded border bg-muted/30 object-contain" />)}</div>
    <p className="text-xs text-muted-foreground">仅展示正文中已上传的图片。客户能否查看仍以文章发布状态和访问权限为准。</p>
  </section>;
}
