import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "./src") } },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:5000",
      "/v2": "http://127.0.0.1:5000",
    },
  },
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false },
});
