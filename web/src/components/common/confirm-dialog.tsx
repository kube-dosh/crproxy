import type { ReactNode } from "react";
import { AlertTriangle, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";

export function ConfirmDialog({ open, onOpenChange, title, description, confirmLabel = "确认", destructive = true, pending = false, onConfirm, children }: { open: boolean; onOpenChange: (open: boolean) => void; title: string; description: ReactNode; confirmLabel?: string; destructive?: boolean; pending?: boolean; onConfirm: () => void; children?: ReactNode }) {
  return <Dialog open={open} onOpenChange={onOpenChange}><DialogContent className="max-w-md"><DialogHeader><div className={`mb-1 grid h-9 w-9 place-items-center rounded-lg ${destructive ? "bg-destructive/10 text-destructive" : "bg-primary/10 text-primary"}`}><AlertTriangle className="h-4 w-4" /></div><DialogTitle>{title}</DialogTitle><DialogDescription>{description}</DialogDescription></DialogHeader>{children}<DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>取消</Button><Button variant={destructive ? "destructive" : "default"} onClick={onConfirm} disabled={pending}>{pending ? <Loader2 className="h-4 w-4 animate-spin" /> : null}{confirmLabel}</Button></DialogFooter></DialogContent></Dialog>;
}
