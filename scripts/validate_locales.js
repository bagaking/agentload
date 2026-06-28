#!/usr/bin/env node

const { spawnSync } = require("node:child_process");
const path = require("node:path");

const rootDir = path.resolve(__dirname, "..");

function run(command, args) {
  const result = spawnSync(command, args, {
    cwd: rootDir,
    stdio: "inherit",
    env: process.env,
  });
  if (result.error) {
    console.error(result.error.message);
    process.exit(1);
  }
  if (result.status !== 0) {
    process.exit(result.status || 1);
  }
}

run("npm", ["--prefix", "ui", "run", "typecheck"]);
run("npm", ["--prefix", "ui", "run", "build"]);
console.log("UI validation passed.");
