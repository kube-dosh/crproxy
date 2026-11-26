import { cn } from "@/lib/utils";

export function Progress({ value, className, tone = "default" }: { value: number; className?: string; tone?: "default" | "warning" | "destructive" | "success" }) {
  const color = tone === "destructive" ? "bg-destructive" : tone === "warning" ? "bg-warning" : tone === "success" ? "bg-success" : "bg-primary";
  return <div className={cn("h-2 w-full overflow-hidden rounded-full bg-muted", className)} role="progressbar" aria-valuenow={Math.round(value)} aria-valuemin={0} aria-valuemax={100}><div className={cn("h-full transition-all", color)} style={{ width: `${Math.max(0, Math.min(100, value))}%` }} /></div>;
}
