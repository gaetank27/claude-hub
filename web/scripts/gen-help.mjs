// Generates ../internal/api/helpdata/help.json from src/lib/help.ts so the Hub's
// GET /api/help serves exactly the chapters of the web manual (single source of
// truth). Run by `npm run build` (before vite, whose emptyOutDir only wipes
// internal/api/assets). help.ts is plain data, so TypeScript is transpiled with
// the `typescript` dev dependency and loaded as an ES module from a data URL.
import ts from "typescript";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const src = fs.readFileSync(path.join(here, "../src/lib/help.ts"), "utf8");
const js = ts.transpileModule(src, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText;
const mod = await import("data:text/javascript;base64," + Buffer.from(js).toString("base64"));

const out = { fr: mod.HELP_CHAPTERS_FR, en: mod.HELP_CHAPTERS_EN };
for (const [lang, chapters] of Object.entries(out)) {
  if (!Array.isArray(chapters) || chapters.length === 0) throw new Error(`no help chapters for ${lang}`);
}
const dir = path.join(here, "../../internal/api/helpdata");
fs.mkdirSync(dir, { recursive: true });
fs.writeFileSync(path.join(dir, "help.json"), JSON.stringify(out) + "\n");
console.log(`help.json: fr=${out.fr.length} en=${out.en.length} chapters`);
