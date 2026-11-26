import { AlertTriangle, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";

export function LoadingState({ label = "正在加载…" }: { label?: string }) {
  return <div className="flex min-h-48 items-center justify-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" />{label}</div>;
}

export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  return (
    <Card className="border-destructive/30"><CardContent className="flex flex-col items-start gap-3 pt-5">
      <div className="flex items-center gap-2 font-medium text-destructive"><AlertTriangle className="h-4 w-4" />加载失败</div>
      <p className="text-sm text-muted-foreground">{error instanceof Error ? error.message : "请稍后重试"}</p>
      {onRetry ? <Button variant="outline" size="sm" onClick={onRetry}>重试</Button> : null}
    </CardContent></Card>
  );
}

export function EmptyState({ title, description, action }: { title: string; description?: string; action?: React.ReactNode }) {
  return <div className="flex min-h-40 flex-col items-center justify-center gap-2 rounded-lg border border-dashed bg-muted/20 p-8 text-center"><p className="font-medium">{title}</p>{description ? <p className="max-w-lg text-sm text-muted-foreground">{description}</p> : null}{action}</div>;
}
