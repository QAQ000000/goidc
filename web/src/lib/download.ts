function safeFilename(value: string): string | undefined {
  const name = value.replace(/[\u0000-\u001f\u007f]/g, "").split(/[\\/]/).pop()?.trim();
  return name && name !== "." && name !== ".." ? name : undefined;
}

// Prefer RFC 5987 UTF-8 names, then a regular filename, then the caller's name.
// A malformed server header must not prevent an otherwise valid file download.
export function downloadFilename(disposition: string | null, fallback = "download"): string {
  let regular: string | undefined;
  let extended: string | undefined;
  const parameters = /(?:^|;)\s*(filename\*?)\s*=\s*("(?:[^"\\]|\\.)*"|[^;]*)/gi;
  for (const match of (disposition ?? "").matchAll(parameters)) {
    let value = match[2].trim();
    if (value.startsWith('"')) {
      if (!value.endsWith('"') || value.length < 2) continue;
      value = value.slice(1, -1).replace(/\\(.)/g, "$1");
    }
    if (match[1].endsWith("*")) {
      const encoded = /^UTF-8'[^']*'(.*)$/i.exec(value);
      if (!encoded) continue;
      try { extended ??= safeFilename(decodeURIComponent(encoded[1])); } catch { /* Use the regular name or fallback. */ }
    } else {
      regular ??= safeFilename(value);
    }
  }
  return extended ?? regular ?? safeFilename(fallback) ?? "download";
}

// Call from browser event handlers after the caller has authorized/fetched data.
export function saveBlob(blob: Blob, filename: string): void {
  const href = URL.createObjectURL(blob);
  let link: HTMLAnchorElement | undefined;
  try {
    link = document.createElement("a");
    link.href = href;
    link.download = downloadFilename(null, filename);
    document.body.appendChild(link);
    link.click();
  } finally {
    try { link?.remove(); }
    finally {
      // Let the browser consume the click before releasing its URL, even if
      // clicking or attaching the temporary element throws.
      window.setTimeout(() => URL.revokeObjectURL(href), 1000);
    }
  }
}
