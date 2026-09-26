import fs from "node:fs"
import path from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig, type Plugin } from "vite"

// Override with FG_BACKEND=http://host:port when the API listens elsewhere.
const backend = process.env.FG_BACKEND ?? "http://127.0.0.1:8080"

// The Go binary embeds web/dist with `//go:embed all:dist`, so the directory
// must never be empty. emptyOutDir wipes the committed .gitkeep on every build;
// write it back once the bundle is on disk.
function keepDistPlaceholder(): Plugin {
  let outDir = "dist"
  return {
    name: "firegateway:keep-dist-placeholder",
    apply: "build",
    configResolved(config) {
      outDir = path.resolve(config.root, config.build.outDir)
    },
    closeBundle() {
      fs.mkdirSync(outDir, { recursive: true })
      fs.writeFileSync(path.join(outDir, ".gitkeep"), "")
    },
  }
}

// https://vite.dev/config/
export default defineConfig({
  base: "/",
  plugins: [react(), tailwindcss(), keepDistPlaceholder()],
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // react-dom + react-router + react-query dominate the entry chunk.
    chunkSizeWarningLimit: 800,
  },
  server: {
    proxy: {
      "/api": { target: backend },
      "/health": { target: backend },
    },
  },
})
