import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// In development the console runs on Vite (port 5173) and talks to a Grantra
// core on port 8080. See web/README.md.
const core = process.env.GRANTRA_CORE_URL ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  server: {
    proxy: {
      "/api": core,
      "/auth": core,
    },
  },
  build: {
    // Keep every script in a file so the server's CSP needs no exceptions.
    modulePreload: { polyfill: false },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
  },
});
