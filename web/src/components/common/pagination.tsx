import { ChevronLeft, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";

export function Pagination({ total, limit, offset, onChange }: { total: number; limit: number; offset: number; onChange: (offset: number) => void }) {
  if (total <= limit) return null;
  const page = Math.floor(offset / limit) + 1;
  const pages = Math.max(1, Math.ceil(total / limit));
  return <div className="flex flex-wrap items-center justify-between gap-2 border-t px-1 pt-3 text-xs text-muted-foreground"><span>共 {total} 条 · 第 {page}/{pages} 页</span><div className="flex gap-2"><Button variant="outline" size="sm" disabled={offset === 0} onClick={() => onChange(Math.max(0, offset - limit))}><ChevronLeft className="h-3.5 w-3.5" />上一页</Button><Button variant="outline" size="sm" disabled={offset + limit >= total} onClick={() => onChange(offset + limit)}>下一页<ChevronRight className="h-3.5 w-3.5" /></Button></div></div>;
}
