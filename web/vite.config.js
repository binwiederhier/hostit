import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const devTarget = process.env.HOSTIT_DEV_TARGET || "http://127.0.0.1:2900";

export default defineConfig({
  base: "/",
  build: {
    outDir: "build",
    assetsDir: "static/media",
    sourcemap: true,
  },
  // `npm run dev` serves the SPA with hot reload but has no API of its own, so
  // everything the app actually talks to is forwarded to a running hostit.
  // Point it somewhere with HOSTIT_DEV_TARGET; the default is a control daemon
  // on this machine with `listen-api: 127.0.0.1:2900` in its config (the main
  // listener routes by hostname and would answer "nothing deployed here"). ws is on
  // for the browser terminal and the assistant's event stream.
  server: {
    port: 3000,
    proxy: {
      "/api": { target: devTarget, changeOrigin: true, ws: true },
      "/auth": { target: devTarget, changeOrigin: true },
    },
  },
  plugins: [react()],
  // Unit tests are the vitest specs under src/; the browser e2e specs under e2e/
  // are Playwright tests (npm run test:e2e) and must not be collected by vitest.
  test: {
    include: ["src/**/*.test.{js,jsx}"],
  },
});
