// Builds the UI into internal/web/dist, which the gnat binary embeds.
import tailwind from "bun-plugin-tailwind";
import { rm } from "node:fs/promises";
import path from "node:path";

const outdir = path.join(import.meta.dir, "../internal/web/dist");
await rm(outdir, { recursive: true, force: true });

const result = await Bun.build({
  entrypoints: [path.join(import.meta.dir, "src/index.html")],
  outdir,
  plugins: [tailwind],
  minify: true,
  target: "browser",
  // Stable names, so a rebuild replaces the checked-in files rather than adding new ones.
  naming: { entry: "[name].[ext]", chunk: "[name].[ext]", asset: "[name].[ext]" },
  define: { "process.env.NODE_ENV": JSON.stringify("production") },
});

for (const output of result.outputs) {
  console.log(` ${path.relative(process.cwd(), output.path)}  ${(output.size / 1024).toFixed(1)} KB`);
}
