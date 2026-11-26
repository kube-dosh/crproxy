import { Badge } from "@/components/ui/badge";

export function StatusBadge({ value, labels }: { value?: string; labels?: Record<string, string> }) {
  const normalized = value || "unknown";
  const variant = normalized === "success" || normalized === "healthy" || normalized === "succeeded" || normalized === "normal" || normalized === "active"
    ? "success"
    : normalized === "failure" || normalized === "failed" || normalized === "unhealthy" || normalized === "critical" || normalized === "emergency" || normalized === "error"
      ? "destructive"
      : normalized === "warning" || normalized === "high" || normalized === "stale" || normalized === "queued" || normalized === "running"
        ? "warning"
        : "secondary";
  const fallback: Record<string, string> = { unknown: "未知", success: "成功", healthy: "健康", unhealthy: "异常", failure: "失败", failed: "失败", error: "错误", queued: "排队中", running: "运行中", succeeded: "已完成", active: "活动", stale: "已过期", normal: "正常", high: "高水位", critical: "临界", emergency: "紧急" };
  return <Badge variant={variant}>{labels?.[normalized] ?? fallback[normalized] ?? normalized}</Badge>;
}
