import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const frontendRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const applications = ["portal", "admin", "usage"];
const staticImportPattern = /(?:^|[;\n])\s*import(?:[^"'`;]*?from\s*)?["']([^"']+)["']/g;
const dynamicImportPattern = /\bimport\(\s*["']([^"']+)["']\s*\)/g;

function localImports(source) {
  const imports = [];
  for (const pattern of [staticImportPattern, dynamicImportPattern]) {
    pattern.lastIndex = 0;
    for (let match = pattern.exec(source); match; match = pattern.exec(source)) {
      if (match[1].startsWith(".")) imports.push(match[1]);
    }
  }
  return imports;
}

function findCycle(graph) {
  const visiting = new Set();
  const visited = new Set();
  const stack = [];

  function visit(node) {
    if (visiting.has(node)) {
      const start = stack.indexOf(node);
      return [...stack.slice(start), node];
    }
    if (visited.has(node)) return null;
    visiting.add(node);
    stack.push(node);
    for (const dependency of graph.get(node) ?? []) {
      const cycle = visit(dependency);
      if (cycle) return cycle;
    }
    stack.pop();
    visiting.delete(node);
    visited.add(node);
    return null;
  }

  for (const node of graph.keys()) {
    const cycle = visit(node);
    if (cycle) return cycle;
  }
  return null;
}

for (const application of applications) {
  const outputRoot = path.join(frontendRoot, "dist", application);
  const html = await readFile(path.join(outputRoot, "index.html"), "utf8");
  if (!html.includes('<div id="root"></div>') || !html.includes('type="module"')) {
    throw new Error(`${application}: built index is missing the React root or module entry`);
  }

  const assetsRoot = path.join(outputRoot, "assets");
  const files = (await readdir(assetsRoot)).filter((name) => name.endsWith(".js"));
  if (files.length === 0) throw new Error(`${application}: built JavaScript assets are missing`);

  const graph = new Map();
  for (const file of files) {
    const absolute = path.join(assetsRoot, file);
    const source = await readFile(absolute, "utf8");
    const dependencies = [];
    for (const specifier of localImports(source)) {
      const resolved = path.resolve(path.dirname(absolute), specifier);
      if (!resolved.startsWith(`${outputRoot}${path.sep}`)) {
        throw new Error(`${application}: asset import escapes its output root: ${specifier}`);
      }
      try {
        await readFile(resolved);
      } catch {
        throw new Error(`${application}: asset import is missing: ${specifier}`);
      }
      if (resolved.endsWith(".js")) dependencies.push(resolved);
    }
    graph.set(absolute, dependencies);
  }

  const cycle = findCycle(graph);
  if (cycle) {
    throw new Error(`${application}: cyclic built JavaScript imports: ${cycle.map((file) => path.basename(file)).join(" -> ")}`);
  }
}

console.log("Built React assets passed root, import, and cycle checks");
