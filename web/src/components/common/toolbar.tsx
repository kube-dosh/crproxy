import type { ReactNode } from "react";

export function Toolbar({ children, actions }: { children: ReactNode; actions?: ReactNode }) {
  return <div className="mb-4 flex flex-col gap-2 rounded-lg border bg-card p-3 shadow-panel sm:flex-row sm:flex-wrap sm:items-center">{children}{actions ? <div className="flex flex-wrap gap-2 sm:ml-auto">{actions}</div> : null}</div>;
}
