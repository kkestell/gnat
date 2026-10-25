// Build the browser UI embedded by the local daemon.
import tailwind from "bun-plugin-tailwind";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

const result = await Bun.build({
  entrypoints: [path.join(import.meta.dir, "src/frontend.tsx")],
  plugins: [tailwind],
  minify: true,
  target: "browser",
  define: { "process.env.NODE_ENV": JSON.stringify("production") },
});
if (!result.success) throw new AggregateError(result.logs, "UI build failed");
const outdir = path.join(import.meta.dir, "../internal/daemon/assets");
await mkdir(outdir, { recursive: true });
for (const output of result.outputs) {
  const ext = path.extname(output.path);
  if (ext !== ".js" && ext !== ".css")
    throw new Error(`Unexpected UI output: ${output.path}`);
  const destination = path.join(outdir, `page${ext}`);
  await writeFile(destination, new Uint8Array(await output.arrayBuffer()));
  console.log(
    `${path.relative(process.cwd(), destination)} ${(output.size / 1024).toFixed(1)} KB`,
  );
}
