import type { SiteTheme } from "./api";

// Legacy /site responses and unavailable theme data retain the existing look.
export const defaultSiteTheme: Readonly<SiteTheme> = {
  template: "default", primary_color: "#15705e", logo_variant: "full", footer_text: "",
};

export function normalizeSiteTheme(value: unknown): SiteTheme {
  const theme = value && typeof value === "object" ? value as Record<string, unknown> : {};
  return {
    template: theme.template === "classic" || theme.template === "minimal" ? theme.template : "default",
    primary_color: typeof theme.primary_color === "string" && /^#[0-9a-f]{6}$/i.test(theme.primary_color) ? theme.primary_color : defaultSiteTheme.primary_color,
    logo_variant: theme.logo_variant === "compact" || theme.logo_variant === "text" ? theme.logo_variant : "full",
    footer_text: typeof theme.footer_text === "string" && Array.from(theme.footer_text).length <= 500 ? theme.footer_text : "",
  };
}

export function siteThemeApplies(pathname: string): boolean {
  return pathname !== "/admin" && !pathname.startsWith("/admin/") && pathname !== "/admin-login" && !pathname.startsWith("/admin-login/");
}

function luminance(color: string): number {
  const channels = [1, 3, 5].map(start => parseInt(color.slice(start, start + 2), 16) / 255);
  return channels.map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4)
    .reduce((sum, value, index) => sum + value * [0.2126, 0.7152, 0.0722][index], 0);
}

function contrast(a: string, b: string): number {
  const values = [luminance(a), luminance(b)];
  return (Math.max(...values) + 0.05) / (Math.min(...values) + 0.05);
}

function readablePrimary(primary: string, backgrounds: string[]): string {
  let color = primary;
  // Links also need contrast on light surfaces when the configured brand color
  // is yellow/white. Buttons keep the original brand color and a black/white label.
  while (backgrounds.some(background => contrast(color, background) < 4.5)) {
    color = `#${[1, 3, 5].map(start => Math.floor(parseInt(color.slice(start, start + 2), 16) * 0.9).toString(16).padStart(2, "0")).join("")}`;
  }
  return color;
}

const presets = {
  default: { background: "#f7f8fa", card: "#ffffff", border: "#e2e4e8", muted: "#f2f3f5", radius: "6px", small: "4px", large: "8px" },
  classic: { background: "#f6f1e8", card: "#fffdf9", border: "#c8beb0", muted: "#eee8de", radius: "0px", small: "0px", large: "2px" },
  minimal: { background: "#ffffff", card: "#ffffff", border: "#dedede", muted: "#f6f6f6", radius: "2px", small: "0px", large: "4px" },
} as const;

export function siteThemeTokens(value: unknown): Record<`--${string}`, string> {
  const theme = normalizeSiteTheme(value);
  const preset = presets[theme.template];
  const link = readablePrimary(theme.primary_color, [preset.background, preset.card]);
  return {
    "--primary": theme.primary_color,
    "--primary-foreground": contrast(theme.primary_color, "#000000") > contrast(theme.primary_color, "#ffffff") ? "#000000" : "#ffffff",
    "--site-primary-text": link,
    "--ring": link,
    "--background": preset.background,
    "--card": preset.card,
    "--popover": preset.card,
    "--border": preset.border,
    "--input": preset.border,
    "--muted": preset.muted,
    "--secondary": preset.muted,
    "--accent": preset.muted,
    "--accent-foreground": link,
    "--radius": preset.radius,
    "--site-surface-radius": preset.radius,
    "--control-radius-sm": preset.small,
    "--control-radius-md": preset.radius,
    "--control-radius-lg": preset.large,
    "--control-radius-xl": preset.large,
  };
}

// Radix portals render outside the wrapper. A route-scoped rule lets them use
// these same tokens without writing inline styles onto body or affecting admin.
export function siteThemePortalCSS(value: unknown): string {
  return `body:has(> .site-theme-scope){${Object.entries(siteThemeTokens(value)).map(([key, token]) => `${key}:${token}`).join(";")}}`;
}
