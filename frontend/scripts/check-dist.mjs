import { readFile, readdir, stat } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

const frontendRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const applications = ["portal", "admin", "usage"];
const staticImportPattern = /(?:^|[;\n])\s*import(?:[^"'`;]*?from\s*)?["']([^"']+)["']/g;
const dynamicImportPattern = /\bimport\(\s*["']([^"']+)["']\s*\)/g;
const builtScriptPattern = /<(?:script|link)\b[^>]*(?:src|href)=["']([^"']+\.js)["'][^>]*>/g;
const maxChunkBytes = 500 * 1024;
const maxInitialBytes = 800 * 1024;
const maxInitialGzipBytes = 260 * 1024;

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

  const initialFiles = new Set();
  builtScriptPattern.lastIndex = 0;
  for (let match = builtScriptPattern.exec(html); match; match = builtScriptPattern.exec(html)) {
    initialFiles.add(path.basename(match[1]));
  }
  if (initialFiles.size === 0) throw new Error(`${application}: initial JavaScript entry is missing`);

  const graph = new Map();
  let initialBytes = 0;
  let initialGzipBytes = 0;
  let includesECharts = false;
  for (const file of files) {
    const absolute = path.join(assetsRoot, file);
    const sourceBuffer = await readFile(absolute);
    const source = sourceBuffer.toString("utf8");
    const size = (await stat(absolute)).size;
    if (size > maxChunkBytes) {
      throw new Error(`${application}: JavaScript chunk exceeds ${maxChunkBytes} bytes: ${file} (${size} bytes)`);
    }
    if (initialFiles.has(file)) {
      initialBytes += size;
      initialGzipBytes += gzipSync(sourceBuffer, { level: 9 }).byteLength;
    }
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

    if (/^(?:echarts|zrender)-vendor-/.test(file)) includesECharts = true;
    try {
      const sourceMap = JSON.parse(await readFile(`${absolute}.map`, "utf8"));
      if ((sourceMap.sources ?? []).some((item) => /node_modules[\\/](?:echarts|zrender)[\\/]/.test(item))) {
        includesECharts = true;
      }
    } catch (error) {
      if (!(error && typeof error === "object" && "code" in error && error.code === "ENOENT")) {
        throw new Error(`${application}: source map is invalid for ${file}: ${error instanceof Error ? error.message : error}`);
      }
    }
  }

  if (initialBytes > maxInitialBytes) {
    throw new Error(`${application}: initial JavaScript exceeds ${maxInitialBytes} bytes (${initialBytes} bytes)`);
  }
  if (initialGzipBytes > maxInitialGzipBytes) {
    throw new Error(`${application}: gzipped initial JavaScript exceeds ${maxInitialGzipBytes} bytes (${initialGzipBytes} bytes)`);
  }
  if (includesECharts !== (application === "admin")) {
    throw new Error(`${application}: ECharts dependency boundary is invalid`);
  }

  const cycle = findCycle(graph);
  if (cycle) {
    throw new Error(`${application}: cyclic built JavaScript imports: ${cycle.map((file) => path.basename(file)).join(" -> ")}`);
  }

  console.log(`${application}: initial ${(initialBytes / 1024).toFixed(1)} KiB raw / ${(initialGzipBytes / 1024).toFixed(1)} KiB gzip`);
}

console.log("Built React assets passed root, import, cycle, size, and dependency-boundary checks");
