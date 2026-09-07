import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { artifactDigest, validateRelease, validationIdentity } from "./release-validation.mjs";

function fixture(t) {
  const root = mkdtempSync(path.join(os.tmpdir(), "cpap-validation-test-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const calls = [];
  let key = "source-and-tools-a";
  let failSource = false;
  let failBrowser = false;
  const options = {
    root, cache: path.join(root, "cache"), platform: "linux/amd64", identity: () => key,
    run(command) {
      calls.push(command);
      if (command === "make") {
        if (failSource) throw new Error("source failed");
        for (const app of ["admin", "portal", "usage"]) {
          const dir = path.join(root, "frontend/dist", app);
          mkdirSync(dir, { recursive: true });
          writeFileSync(path.join(dir, "index.html"), `<html>${key}/${app}</html>`);
        }
      } else if (failBrowser) throw new Error("browser failed");
    }
  };
  return { root, calls, options, setKey: (value) => { key = value; }, failSource: () => { failSource = true; }, failBrowser: (value) => { failBrowser = value; } };
}

test("a publication retry reuses both gates and restores exactly the validated frontend", (t) => {
  const f = fixture(t);
  validateRelease(f.options);
  const expected = artifactDigest(path.join(f.root, "frontend/dist"));
  rmSync(path.join(f.root, "frontend/dist"), { recursive: true });
  validateRelease(f.options);
  assert.deepEqual(f.calls, ["make", "npm"]);
  assert.equal(artifactDigest(path.join(f.root, "frontend/dist")), expected);
  assert.equal(validateRelease({ ...f.options, checkOnly: true }).browser, true);
});

test("source or toolchain changes invalidate both gates", (t) => {
  const f = fixture(t);
  validateRelease(f.options);
  f.setKey("source-and-tools-b");
  assert.throws(() => validateRelease({ ...f.options, checkOnly: true }));
  validateRelease(f.options);
  assert.deepEqual(f.calls, ["make", "npm", "make", "npm"]);
});

test("a failed browser gate is retried without repeating successful source verification", (t) => {
  const f = fixture(t);
  f.failBrowser(true);
  assert.throws(() => validateRelease(f.options), /browser failed/);
  assert.throws(() => validateRelease({ ...f.options, checkOnly: true }));
  f.failBrowser(false);
  validateRelease(f.options);
  assert.deepEqual(f.calls, ["make", "npm", "npm"]);
});

test("source failure never produces a reusable receipt", (t) => {
  const f = fixture(t);
  f.failSource();
  assert.throws(() => validateRelease(f.options), /source failed/);
  assert.throws(() => validateRelease({ ...f.options, checkOnly: true }));
});

test("tampered cached output reruns validation; tampered working output blocks image publication", (t) => {
  const f = fixture(t);
  validateRelease(f.options);
  const local = path.join(f.root, "frontend/dist/usage/index.html");
  writeFileSync(local, "modified");
  assert.throws(() => validateRelease({ ...f.options, checkOnly: true }));
  const cached = path.join(f.options.cache, "source-and-tools-a/dist/usage/index.html");
  writeFileSync(cached, "modified");
  validateRelease(f.options);
  assert.deepEqual(f.calls, ["make", "npm", "make", "npm"]);
  assert.notEqual(readFileSync(local, "utf8"), "modified");
});

test("symlinks cannot be restored or accepted as static release output", (t) => {
  const f = fixture(t);
  validateRelease(f.options);
  symlinkSync(f.root, path.join(f.root, "frontend/dist/escape"));
  assert.throws(() => validateRelease({ ...f.options, checkOnly: true }), /符号链接/);
});

test("concurrent validations for the same inputs fail before either gate runs", (t) => {
  const f = fixture(t);
  mkdirSync(path.join(f.options.cache, "source-and-tools-a.lock"), { recursive: true });
  assert.throws(() => validateRelease(f.options), /正在验收/);
  assert.deepEqual(f.calls, []);
});

test("identity covers source, actual browser binary, toolchain and target platform and rejects dirty source", (t) => {
  const f = fixture(t);
  const module = path.join(f.root, "frontend/node_modules/@playwright/test");
  mkdirSync(module, { recursive: true });
  const browser = path.join(f.root, "browser");
  writeFileSync(browser, "chromium-a");
  writeFileSync(path.join(module, "index.js"), `exports.chromium = { executablePath: () => ${JSON.stringify(browser)} };`);
  let dirty = false;
  let tree = "tree-a";
  let go = "go-a";
  const run = (command, args) => command === "git"
    ? (args[0] === "status" ? (dirty ? " M source" : "") : tree)
    : command === "go" ? go : "version-a";
  const identity = () => validationIdentity(f.root, "linux/amd64", run);
  const initial = identity();
  assert.equal(initial, identity());
  tree = "tree-b"; assert.notEqual(initial, identity()); tree = "tree-a";
  go = "go-b"; assert.notEqual(initial, identity()); go = "go-a";
  assert.notEqual(initial, validationIdentity(f.root, "linux/arm64", run));
  writeFileSync(browser, "chromium-b"); assert.notEqual(initial, identity());
  dirty = true; assert.throws(identity, /干净/);
});


test("the command entry runs through a symlink, including macOS /var aliases", (t) => {
  const f = fixture(t);
  const alias = path.join(f.root, "validate.mjs");
  symlinkSync(fileURLToPath(new URL("./release-validation.mjs", import.meta.url)), alias);
  const result = spawnSync(process.execPath, [alias], { encoding: "utf8" });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /usage: release-validation/);
});
