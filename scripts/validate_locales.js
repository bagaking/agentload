#!/usr/bin/env node

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const rootDir = path.resolve(__dirname, "..");
const mainSourcePath = path.join(rootDir, "ui", "src", "main.tsx");
const copySourcePath = path.join(rootDir, "ui", "src", "i18n.ts");
const locales = ["en", "zh", "ja"];

function fail(message) {
  console.error(message);
  process.exit(1);
}

function findBalancedBlock(source, openIndex) {
  let depth = 0;
  let quote = "";
  let escaped = false;
  let lineComment = false;
  let blockComment = false;

  for (let index = openIndex; index < source.length; index += 1) {
    const char = source[index];
    const next = source[index + 1];

    if (lineComment) {
      if (char === "\n") lineComment = false;
      continue;
    }
    if (blockComment) {
      if (char === "*" && next === "/") {
        blockComment = false;
        index += 1;
      }
      continue;
    }
    if (quote) {
      if (escaped) {
        escaped = false;
      } else if (char === "\\") {
        escaped = true;
      } else if (char === quote) {
        quote = "";
      }
      continue;
    }
    if (char === "/" && next === "/") {
      lineComment = true;
      index += 1;
      continue;
    }
    if (char === "/" && next === "*") {
      blockComment = true;
      index += 1;
      continue;
    }
    if (char === "\"" || char === "'" || char === "`") {
      quote = char;
      continue;
    }
    if (char === "{") {
      depth += 1;
      continue;
    }
    if (char === "}") {
      depth -= 1;
      if (depth === 0) return source.slice(openIndex + 1, index);
    }
  }

  fail("Could not find matching brace in locale copy source.");
}

function extractCopyObject(source) {
  const declarationIndex = source.indexOf("const copy");
  if (declarationIndex < 0) fail("Missing copy object in UI source.");
  const openIndex = source.indexOf("{", declarationIndex);
  if (openIndex < 0) fail("Missing copy object body in UI source.");
  return findBalancedBlock(source, openIndex);
}

function extractLocaleBlock(copyBody, locale) {
  const match = new RegExp(`\\b${locale}\\s*:`).exec(copyBody);
  if (!match) fail(`Missing ${locale} locale in copy object.`);
  const openIndex = copyBody.indexOf("{", match.index);
  if (openIndex < 0) fail(`Missing ${locale} locale body in copy object.`);
  return findBalancedBlock(copyBody, openIndex);
}

function parseLocale(locale, body) {
  const values = new Map();
  const pairPattern = /\b([A-Za-z][A-Za-z0-9_]*)\s*:\s*"((?:\\.|[^"\\])*)"/g;
  let match;
  while ((match = pairPattern.exec(body)) !== null) {
    const key = match[1];
    if (values.has(key)) fail(`Duplicate ${locale}.${key} copy key.`);
    values.set(key, JSON.parse(`"${match[2]}"`));
  }
  if (!values.size) fail(`No string keys found for ${locale} locale.`);
  return values;
}

function placeholders(value) {
  return Array.from(value.matchAll(/\{[A-Za-z0-9_]+\}/g), (match) => match[0]).sort();
}

function equalArray(left, right) {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function validateCopy() {
  const source = fs.readFileSync(copySourcePath, "utf8");
  const mainSource = fs.readFileSync(mainSourcePath, "utf8");
  const copyBody = extractCopyObject(source);
  const copy = new Map(locales.map((locale) => [locale, parseLocale(locale, extractLocaleBlock(copyBody, locale))]));
  const baseline = copy.get("en");
  const baselineKeys = Array.from(baseline.keys()).sort();
  const errors = [];

  for (const locale of locales) {
    const values = copy.get(locale);
    const keys = Array.from(values.keys()).sort();
    const missing = baselineKeys.filter((key) => !values.has(key));
    const extra = keys.filter((key) => !baseline.has(key));
    if (missing.length) errors.push(`${locale} missing keys: ${missing.join(", ")}`);
    if (extra.length) errors.push(`${locale} extra keys: ${extra.join(", ")}`);

    for (const key of baselineKeys) {
      if (!values.has(key)) continue;
      const expected = placeholders(baseline.get(key));
      const actual = placeholders(values.get(key));
      if (!equalArray(expected, actual)) {
        errors.push(`${locale}.${key} placeholders ${actual.join(" ") || "(none)"} do not match en ${expected.join(" ") || "(none)"}`);
      }
    }
  }

  const referenced = new Set();
  const referencePattern = /\bt\(\s*["'`]([A-Za-z][A-Za-z0-9_]*)["'`]\s*\)/g;
  let referenceMatch;
  while ((referenceMatch = referencePattern.exec(mainSource)) !== null) {
    referenced.add(referenceMatch[1]);
  }
  const missingReferences = Array.from(referenced).filter((key) => !baseline.has(key)).sort();
  if (missingReferences.length) errors.push(`Referenced copy keys missing from en: ${missingReferences.join(", ")}`);

  if (errors.length) {
    errors.forEach((error) => console.error(`- ${error}`));
    process.exit(1);
  }
  console.log(`Locale copy validation passed (${baselineKeys.length} keys across ${locales.length} locales).`);
}

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

validateCopy();
run("npm", ["--prefix", "ui", "run", "typecheck"]);
run("npm", ["--prefix", "ui", "run", "build"]);
console.log("UI validation passed.");
