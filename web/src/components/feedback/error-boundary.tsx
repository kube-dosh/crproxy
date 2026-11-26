import { Component, type ErrorInfo, type ReactNode } from "react";
import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";

type State = { error: Error | null };
export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null };
  static getDerivedStateFromError(error: Error): State { return { error }; }
  componentDidCatch(error: Error, info: ErrorInfo) { console.error("crproxy UI error", error, info.componentStack); }
  render() {
    if (!this.state.error) return this.props.children;
    return <div className="grid min-h-screen place-items-center p-6"><Card className="w-full max-w-lg border-destructive/30"><CardContent className="pt-6"><AlertTriangle className="h-8 w-8 text-destructive" /><h1 className="mt-4 text-lg font-semibold">管理台发生未处理错误</h1><p className="mt-2 break-all text-sm text-muted-foreground">{this.state.error.message}</p><Button className="mt-5" onClick={() => window.location.reload()}>重新加载</Button></CardContent></Card></div>;
  }
}
