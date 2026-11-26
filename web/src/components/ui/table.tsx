import * as React from "react";
import { cn } from "@/lib/utils";

const Table = React.forwardRef<HTMLTableElement, React.HTMLAttributes<HTMLTableElement>>(({ className, ...props }, ref) => (
  <div className="w-full max-w-full">
    <div className="flex items-center justify-between border-b bg-muted/20 px-3 py-2 text-[11px] text-muted-foreground lg:hidden"><span>左右滑动查看完整列</span><span aria-hidden="true">← →</span></div>
    <div role="region" aria-label="可滚动表格" tabIndex={0} className="scrollbar-thin relative w-full max-w-full overflow-auto overscroll-x-contain rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
      <table ref={ref} className={cn("w-full min-w-[680px] caption-bottom text-sm", className)} {...props} />
    </div>
  </div>
));
Table.displayName = "Table";
const TableHeader = React.forwardRef<HTMLTableSectionElement, React.HTMLAttributes<HTMLTableSectionElement>>(({ className, ...props }, ref) => <thead ref={ref} className={cn("bg-muted/40 [&_tr]:border-b", className)} {...props} />);
const TableBody = React.forwardRef<HTMLTableSectionElement, React.HTMLAttributes<HTMLTableSectionElement>>(({ className, ...props }, ref) => <tbody ref={ref} className={cn("[&_tr:last-child]:border-0", className)} {...props} />);
const TableRow = React.forwardRef<HTMLTableRowElement, React.HTMLAttributes<HTMLTableRowElement>>(({ className, ...props }, ref) => <tr ref={ref} className={cn("border-b transition-colors hover:bg-muted/35", className)} {...props} />);
const TableHead = React.forwardRef<HTMLTableCellElement, React.ThHTMLAttributes<HTMLTableCellElement>>(({ className, ...props }, ref) => <th ref={ref} className={cn("soft-wrap h-10 whitespace-nowrap px-4 text-left align-middle text-xs font-medium uppercase tracking-wide text-muted-foreground", className)} {...props} />);
const TableCell = React.forwardRef<HTMLTableCellElement, React.TdHTMLAttributes<HTMLTableCellElement>>(({ className, ...props }, ref) => <td ref={ref} className={cn("soft-wrap h-11 min-w-0 whitespace-nowrap px-4 align-middle", className)} {...props} />);
const TableEmpty = ({ colSpan, children }: { colSpan: number; children?: React.ReactNode }) => <TableRow><TableCell colSpan={colSpan} className="h-40 text-center text-muted-foreground">{children ?? "暂无数据"}</TableCell></TableRow>;
export { Table, TableHeader, TableBody, TableHead, TableRow, TableCell, TableEmpty };
