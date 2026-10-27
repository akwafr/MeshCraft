import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      // `ws: true` is what lets the dev server carry a WebSocket UPGRADE, and
      // without it three features of this product are unusable under `make
      // web-dev` while working perfectly in the built app: the device console,
      // the shared serial line, and the live site stream. The failure is not a
      // clear refusal either — the socket closes and the terminal reports "the
      // connection to the controller was lost", which reads as a broken
      // controller rather than a proxy that does not forward upgrades.
      "/api": { target: "http://localhost:8080", ws: true },
      "/downloads": "http://localhost:8080",
    },
  },
});
