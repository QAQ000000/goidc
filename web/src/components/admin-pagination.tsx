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
}: {
  total: number;
  page: number; // 0-indexed
  pageSize: number;
  onPageChange: (newPage: number) => void;
  onPageSizeChange?: (newPageSize: number) => void;
}) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const [jumpPage, setJumpPage] = useState("");

  function handleJump(e: React.FormEvent) {
    e.preventDefault();
    const target = parseInt(jumpPage || String(page + 1), 10);
    if (!isNaN(target) && target >= 1 && target <= totalPages) {
      onPageChange(target - 1);
      setJumpPage("");
    }
  }

  return (
    <div className="flex flex-wrap items-center justify-between gap-3 text-xs text-[#606266] pt-2">
      <div>共 {total} 条</div>
      <div className="flex items-center gap-3">
        {onPageSizeChange && (
          <Select
            value={String(pageSize)}
            onValueChange={(val) => {
              onPageSizeChange(Number(val));
            }}
          >
            <SelectTrigger className="h-7 text-xs w-24 border-[#dcdfe6]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="25">25条/页</SelectItem>
              <SelectItem value="50">50条/页</SelectItem>
              <SelectItem value="100">100条/页</SelectItem>
            </SelectContent>
          </Select>
        )}

        <div className="flex items-center gap-1">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={page === 0}
            onClick={() => onPageChange(page - 1)}
            className="h-7 px-2 text-xs border-[#dcdfe6]"
          >
            &lt;
          </Button>
          <span className="px-2 py-1 bg-[#409eff] text-white rounded font-medium text-xs">
            {page + 1}
          </span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={page + 1 >= totalPages}
            onClick={() => onPageChange(page + 1)}
            className="h-7 px-2 text-xs border-[#dcdfe6]"
          >
            &gt;
          </Button>
        </div>

        <form onSubmit={handleJump} className="flex items-center gap-1.5">
          <span>前往</span>
          <Input
            type="number"
            min={1}
            max={totalPages}
            placeholder={String(page + 1)}
            value={jumpPage}
            onChange={(e) => setJumpPage(e.target.value)}
            className="h-7 w-12 text-xs text-center border-[#dcdfe6] p-0"
          />
          <span>页</span>
        </form>
      </div>
    </div>
  );
}
