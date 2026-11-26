import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, setCSRFToken, unauthorizedEvent } from "@/api/client";
import { navigate } from "@/lib/router";
import type { Session } from "@/api/types";

export const sessionQueryKey = ["session"] as const;

export function useSessionQuery() {
  return useQuery<Session>({
    queryKey: sessionQueryKey,
    queryFn: async () => {
      const session = await api.session();
      setCSRFToken(session.csrfToken);
      return session;
    },
    retry: false,
    staleTime: 30_000,
  });
}

export function useUnauthorizedRedirect() {
  const queryClient = useQueryClient();
  useEffect(() => {
    const handler = () => {
      setCSRFToken("");
      queryClient.clear();
      if (window.location.pathname !== "/login") navigate("/login", { replace: true });
    };
    window.addEventListener(unauthorizedEvent, handler);
    return () => window.removeEventListener(unauthorizedEvent, handler);
  }, [queryClient]);
}

export function useResetSession() {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: sessionQueryKey });
}
