"use client";

export function AdminStatusTabs({ value, onChange, states, label }: { value: string; onChange: (value: string) => void; states: Record<string, string>; label: string }) {
  return <div className="mb-4 flex overflow-x-auto border-b" role="group" aria-label={label}>{Object.entries({ all: "全部", ...states }).map(([key, text]) => <button key={key} type="button" aria-pressed={value === key} onClick={() => onChange(key)} className={`shrink-0 border-b-2 px-5 py-3 text-sm transition-colors ${value === key ? "border-primary font-medium text-primary" : "border-transparent text-muted-foreground hover:text-primary"}`}>{text}</button>)}</div>;
}
