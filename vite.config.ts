import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The build lands in internal/web/dist, which the Go binary embeds, so one
// process serves the UI, the API and the WebSocket from the same origin. In
// development the Vite server proxies API and WebSocket traffic to Go.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "internal/web/dist", emptyOutDir: true },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/ws": { target: "ws://localhost:8080", ws: true },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: "./src/test/setup.ts",
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
