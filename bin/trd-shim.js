#!/usr/bin/env node
// Thin JS shim: execs the native trd binary postinstall placed at bin/trd.
"use strict";
const { spawnSync } = require("node:child_process");
const path = require("node:path");

const binary = path.join(__dirname, "trd");
const { status, error } = spawnSync(binary, process.argv.slice(2), {
  stdio: "inherit",
});
if (error) {
  if (error.code === "ENOENT") {
    console.error(
      "[trd] native binary not found at",
      binary,
      "\nRun `node postinstall.js` from the package dir, or build: go build -o bin/trd ./cmd/trd",
    );
  } else {
    console.error("[trd]", error.message);
  }
  process.exit(1);
}
process.exit(status ?? 0);
