import type { LucideIcon } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";

export function MetricCard({ label, value, hint, icon: Icon, tone = "default" }: { label: string; value: string; hint?: string; icon: LucideIcon; tone?: "default" | "success" | "warning" | "destructive" }) {
  const color = tone === "success" ? "text-success bg-success/10" : tone === "warning" ? "text-warning bg-warning/10" : tone === "destructive" ? "text-destructive bg-destructive/10" : "text-primary bg-primary/10";
  return (
    <Card><CardContent className="flex items-start justify-between gap-4 pt-5">
      <div className="min-w-0"><p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</p><p className="mt-2 truncate text-2xl font-semibold tracking-tight">{value}</p>{hint ? <p className="mt-1 truncate text-xs text-muted-foreground">{hint}</p> : null}</div>
      <div className={`grid h-9 w-9 shrink-0 place-items-center rounded-lg ${color}`}><Icon className="h-4 w-4" /></div>
    </CardContent></Card>
  );
}
