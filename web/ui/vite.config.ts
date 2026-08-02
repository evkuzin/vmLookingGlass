import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    // Output is embedded into the Go binary.
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    // Vite serves the UI in development while the daemon serves the API.
    proxy: {
      "/api": "http://localhost:8880",
    },
  },
});
