import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { App } from "@/App";
import { ErrorBoundary } from "@/components/feedback/error-boundary";
import { ThemeProvider } from "@/lib/theme";
import "@/index.css";

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false, staleTime: 10_000 }, mutations: { retry: 0 } } });
createRoot(document.getElementById("root")!).render(<StrictMode><ErrorBoundary><QueryClientProvider client={queryClient}><ThemeProvider><App /><Toaster richColors position="top-right" closeButton /></ThemeProvider></QueryClientProvider></ErrorBoundary></StrictMode>);
