import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

// The build is emitted straight into the Go backend's embed directory so a
// single `go build` produces a self-contained binary.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: path.resolve(__dirname, "../internal/api/assets"),
    emptyOutDir: true,
  },
  server: {
    // `npm run dev` proxies API/WS calls to the Go backend.
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8787",
        ws: true,
      },
    },
  },
});
