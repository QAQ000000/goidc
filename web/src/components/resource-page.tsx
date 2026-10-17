"use client";
import { Search, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export function Stat({
  icon: Icon,
  label,
  value,
}: {
  icon: typeof Users;
  label: string;
  value: number | string;
}) {
  return (
    <div className="stat">
      <div className="flex gap-2 items-center text-muted-foreground">
        <Icon size={16} />
        {label}
      </div>
      <strong>{typeof value === "number" ? value.toLocaleString("zh-CN") : value}</strong>
    </div>
  );
}

export function PageHeading({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children?: React.ReactNode;
}) {
  return (
    <div className="admin-page-heading flex flex-wrap justify-between items-start gap-4 mb-8">
      <div className="min-w-0">
        <h1 className="page-title break-all">{title}</h1>
        <p className="text-muted-foreground mt-2">{description}</p>
      </div>
      {children}
    </div>
  );
}

export function SearchForm({
  value,
  setValue,
  submit,
  placeholder,
}: {
  value: string;
  setValue: (s: string) => void;
  submit: () => void;
  placeholder: string;
}) {
  return (
    <form
      className="flex gap-2 mb-6 max-w-md"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <Input
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={placeholder}
        aria-label={placeholder}
      />
      <Button variant="outline" size="icon" aria-label="搜索">
        <Search size={16} />
      </Button>
    </form>
  );
}
