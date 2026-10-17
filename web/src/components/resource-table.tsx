"use client";

import type { Key, ReactNode } from "react";
import { ArrowLeft, ArrowRight } from "lucide-react";
import { Button } from "./ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "./ui/table";

// Screens own fetching, URL state, permissions and mutations; this owns markup.
export function ResourceTable<T>({
  items,
  rowKey,
  columns,
  headers: suppliedHeaders,
  renderRow: suppliedRenderRow,
  empty = "暂无记录",
  className = "admin-list-surface data-surface overflow-auto",
}: {
  items: readonly T[];
  rowKey: (item: T, index: number) => Key;
  empty?: ReactNode;
  className?: string;
} & (
  | {
      columns: readonly {
        header: ReactNode;
        render: (item: T) => ReactNode;
        className?: string;
      }[];
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
          <TableCell key={index} className={column.className}>
            {column.render(item)}
          </TableCell>
        ))
    : suppliedRenderRow;
  return (
    <div className={className}>
      <Table>
        <TableHeader>
          <TableRow>
            {headers.map((header, index) => (
              <TableHead key={index}>{header}</TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.length ? (
            items.map((item, index) => (
              <TableRow key={rowKey(item, index)}>{renderRow(item)}</TableRow>
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
