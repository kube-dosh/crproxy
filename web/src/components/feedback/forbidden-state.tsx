import { ShieldX } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { navigate } from "@/lib/router";

export function ForbiddenState({ title = "没有访问权限", description = "当前账号角色不允许访问此页面。" }: { title?: string; description?: string }) {
  return <div className="grid min-h-[55vh] place-items-center"><Card className="w-full max-w-lg"><CardContent className="flex flex-col items-center pt-8 text-center"><ShieldX className="h-10 w-10 text-muted-foreground" /><h1 className="mt-4 text-lg font-semibold">{title}</h1><p className="mt-2 text-sm text-muted-foreground">{description}</p><Button className="mt-5" variant="outline" onClick={() => navigate("/")}>返回总览</Button></CardContent></Card></div>;
}

export function NotFoundState() {
  return <div className="grid min-h-[55vh] place-items-center"><div className="text-center"><p className="text-6xl font-semibold tracking-tight text-muted-foreground/40">404</p><h1 className="mt-3 text-lg font-semibold">页面不存在</h1><p className="mt-2 text-sm text-muted-foreground">请检查地址，或从左侧导航重新进入。</p><Button className="mt-5" variant="outline" onClick={() => navigate("/")}>返回总览</Button></div></div>;
}
