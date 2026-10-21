"use client";

import type { Key, ReactNode } from "react";
import { ArrowLeft, ArrowRight } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "./ui/table";

export type ResourceColumn<T> = {
  id?: string;
  header: ReactNode;
  render: (item: T) => ReactNode;
  className?: string;
  headerClassName?: string;
};

// Screens own fetching, URL state, permissions and mutations; this owns markup.
export function ResourceTable<T>({
  items,
  rowKey,
  columns,
  headers: suppliedHeaders,
  renderRow: suppliedRenderRow,
  empty = "暂无记录",
  className = "admin-list-surface data-surface overflow-auto",
  ariaLabel,
  tableClassName,
  headerClassName,
  rowClassName,
  renderMobile,
}: {
  items: readonly T[];
  rowKey: (item: T, index: number) => Key;
  empty?: ReactNode;
  className?: string;
  ariaLabel?: string;
  tableClassName?: string;
  headerClassName?: string;
  rowClassName?: string | ((item: T) => string);
  renderMobile?: (item: T) => ReactNode;
} & (
  | {
      columns: readonly ResourceColumn<T>[];
      headers?: never;
      renderRow?: never;
    }
  | {
      columns?: never;
      headers: readonly ReactNode[];
      renderRow: (item: T) => ReactNode;
    }
)) {
  const headers = columns
    ? columns.map((column) => column.header)
    : suppliedHeaders;
  const renderRow = columns
    ? (item: T) =>
        columns.map((column, index) => (
          <TableCell key={column.id ?? index} className={column.className}>
            {column.render(item)}
          </TableCell>
        ))
    : suppliedRenderRow;
  const table = (
    <Table aria-label={ariaLabel} className={tableClassName}>
      <TableHeader>
        <TableRow>
          {headers.map((header, index) => (
            <TableHead key={columns?.[index].id ?? index} className={cn(headerClassName, columns?.[index].headerClassName)}>{header}</TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {items.length ? (
          items.map((item, index) => (
            <TableRow key={rowKey(item, index)} className={typeof rowClassName === "function" ? rowClassName(item) : rowClassName}>{renderRow(item)}</TableRow>
          ))
        ) : (
          <TableRow>
            <TableCell
              colSpan={headers.length}
              className="h-28 text-center text-muted-foreground"
            >
              {empty}
            </TableCell>
          </TableRow>
        )}
      </TableBody>
    </Table>
  );
  return (
    <div className={className}>
      {!renderMobile ? table : !items.length ? <div role="status" className="px-4 py-14 text-center text-sm text-muted-foreground">{empty}</div> : <>
        <div className="hidden md:block">{table}</div>
        <div role="list" aria-label={ariaLabel} className="divide-y divide-border md:hidden">{items.map((item, index) => <div role="listitem" key={rowKey(item, index)}>{renderMobile(item)}</div>)}</div>
      </>}
    </div>
  );
}

export function ResourcePagination({
  total,
  offset,
  limit,
  onOffsetChange,
  unit,
  children,
}: {
  total: number;
  offset: number;
  limit: number;
  onOffsetChange: (offset: number) => void;
  unit: string;
  children?: ReactNode;
}) {
  return (
    <div className="mt-5 flex flex-wrap items-center justify-between gap-3 text-sm">
      {children}
      <span>
        {total} 个{unit}
      </span>
      <div className="flex gap-2">
        <Button
          variant="outline"
          disabled={offset === 0}
          onClick={() => onOffsetChange(Math.max(0, offset - limit))}
        >
          <ArrowLeft />
          上一页
        </Button>
        <Button
          variant="outline"
          disabled={offset + limit >= total}
          onClick={() => onOffsetChange(offset + limit)}
        >
          下一页
          <ArrowRight />
        </Button>
      </div>
    </div>
  );
}

export function DetailFields({
  fields,
  className = "grid sm:grid-cols-2 gap-3 text-sm",
}: {
  fields: Record<string, ReactNode>;
  className?: string;
}) {
  return (
    <dl className={className}>
      {Object.entries(fields).map(
        ([label, value]) =>
          value !== undefined &&
          value !== null &&
          value !== "" && (
            <div className="min-w-0" key={label}>
              <dt className="text-muted-foreground">{label}</dt>
              <dd className="break-all">{value}</dd>
            </div>
          ),
      )}
    </dl>
  );
}
