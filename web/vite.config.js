import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/",
  build: {
    outDir: "build",
    assetsDir: "static/media",
    sourcemap: true,
  },
  server: {
    port: 3000,
  },
  plugins: [react()],
  // Unit tests are the vitest specs under src/; the browser e2e specs under e2e/
  // are Playwright tests (npm run test:e2e) and must not be collected by vitest.
  test: {
    include: ["src/**/*.test.{js,jsx}"],
  },
});
