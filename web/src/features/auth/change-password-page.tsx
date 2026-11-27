import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Loader2, LogOut } from "lucide-react";
import { toast } from "sonner";
import { api, errorMessage, setCSRFToken } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { navigate } from "@/lib/router";

export function ChangePasswordPage({ initial = false }: { initial?: boolean }) {
  const queryClient = useQueryClient();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const mutation = useMutation({
    mutationFn: () => api.changePassword(currentPassword, newPassword),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["session"] });
      navigate("/");
    },
  });
  const mismatch = confirmPassword.length > 0 && newPassword !== confirmPassword;
  return (
    <div className="mx-auto flex min-h-[70vh] max-w-lg items-center">
      <Card className="w-full"><CardHeader><div className="mb-2 grid h-9 w-9 place-items-center rounded-lg bg-primary/10 text-primary"><KeyRound className="h-4 w-4" /></div><CardTitle>{initial ? "首次登录：更新管理员密码" : "修改密码"}</CardTitle><CardDescription>{initial ? "初始管理员密码仅用于首次引导，必须先修改后才能继续使用管理台。" : "修改成功后，其他已登录会话会被立即撤销。"}</CardDescription></CardHeader>
        <CardContent>
          <form className="space-y-4" onSubmit={(event) => { event.preventDefault(); if (!mismatch) mutation.mutate(); }}>
            <div className="space-y-1.5"><Label htmlFor="current-password">当前密码</Label><Input id="current-password" type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} /></div>
            <div className="space-y-1.5"><Label htmlFor="new-password">新密码</Label><Input id="new-password" type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} placeholder="至少 12 个字符" /></div>
            <div className="space-y-1.5"><Label htmlFor="confirm-password">确认新密码</Label><Input id="confirm-password" type="password" autoComplete="new-password" value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} /></div>
            {mismatch ? <p className="text-sm text-destructive">两次输入的新密码不一致</p> : null}
            {mutation.error ? <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{errorMessage(mutation.error)}</div> : null}
            <div className="flex flex-wrap gap-2"><Button type="submit" disabled={mutation.isPending || newPassword.length < 12 || mismatch}>{mutation.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <KeyRound className="h-4 w-4" />}更新密码</Button>{!initial ? <Button type="button" variant="outline" onClick={async () => { try { await api.logout(); setCSRFToken(""); window.location.replace("/login"); } catch (error) { toast.error(`退出登录失败：${errorMessage(error)}`); } }}><LogOut className="h-4 w-4" />退出登录</Button> : null}</div>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
