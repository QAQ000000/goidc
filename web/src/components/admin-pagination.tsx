"use client";

import { useState } from "react";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";

export function AdminPagination({
  total,
  page,
  pageSize,
  onPageChange,
  onPageSizeChange,
  pageSizes = [25, 50, 100],
}: {
  total: number;
  page: number; // 0-indexed
  pageSize: number;
  onPageChange: (newPage: number) => void;
  onPageSizeChange?: (newPageSize: number) => void;
  pageSizes?: readonly number[];
}) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const [jumpPage, setJumpPage] = useState("");

  function handleJump(e: React.FormEvent) {
    e.preventDefault();
    const value = jumpPage.trim() || String(page + 1);
    const target = Number(value);
    if (/^[1-9]\d*$/.test(value) && Number.isSafeInteger(target) && target <= totalPages) {
      onPageChange(target - 1);
      setJumpPage("");
    }
  }

  return (
    <nav aria-label="列表分页" className="flex min-w-0 flex-wrap items-center justify-between gap-3 pt-2 text-xs text-muted-foreground">
      <p className="tabular-nums">共 {total} 条</p>
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        {onPageSizeChange && (
          <Select
            value={String(pageSize)}
            onValueChange={(val) => {
              onPageSizeChange(Number(val));
            }}
          >
            <SelectTrigger aria-label="每页记录数" className="h-11 w-28 rounded border-border bg-card text-xs md:h-9">
              <SelectValue>{pageSize}条/页</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {Array.from(new Set([...pageSizes, pageSize])).filter(size => Number.isSafeInteger(size) && size > 0).sort((a, b) => a - b).map(size => <SelectItem key={size} value={String(size)}>{size}条/页</SelectItem>)}
            </SelectContent>
          </Select>
        )}

        <div className="flex items-center gap-1">
          <Button
            type="button"
            variant="outline"
            size="sm"
            aria-label="上一页"
            disabled={page <= 0}
            onClick={() => { if (page > 0) onPageChange(page - 1); }}
            className="h-11 min-w-11 rounded border-border px-3 text-xs md:h-9 md:min-w-9"
          >
            &lt;
          </Button>
          <span aria-current="page" aria-label={`第 ${page + 1} 页，共 ${totalPages} 页`} className="rounded bg-accent px-3 py-2 text-xs font-medium tabular-nums text-primary">
            {page + 1} / {totalPages}
          </span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            aria-label="下一页"
            disabled={page + 1 >= totalPages}
            onClick={() => { if (page + 1 < totalPages) onPageChange(page + 1); }}
            className="h-11 min-w-11 rounded border-border px-3 text-xs md:h-9 md:min-w-9"
          >
            &gt;
          </Button>
        </div>

        <form onSubmit={handleJump} aria-label="跳转页码" className="flex items-center gap-1.5">
          <Input
            type="number"
            aria-label="目标页码"
            inputMode="numeric"
            min={1}
            max={totalPages}
            step={1}
            placeholder={String(page + 1)}
            value={jumpPage}
            onChange={(e) => setJumpPage(e.target.value)}
            className="h-11 w-16 rounded border-border bg-card px-1 text-center text-xs md:h-9"
          />
          <Button type="submit" variant="outline" className="h-11 rounded border-border px-3 text-xs md:h-9">跳转</Button>
        </form>
      </div>
    </nav>
  );
}
