#!/usr/bin/env node

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const rootDir = path.resolve(__dirname, "..");
const uiSourceDir = path.join(rootDir, "ui", "src");
const copySourcePath = path.join(uiSourceDir, "i18n.ts");
const locales = ["en", "zh", "ja"];

// Keys assembled at runtime by prefix concatenation. Source of truth:
// ui/src/diagnostics/diagnosticModel.ts (translateDiagnosticKey and
// diagnosticOmittedFieldLabel). A key counts as dynamically referenced only
// when it is `prefix + <NonEmptyMiddle> + suffix`, so plain keys that merely
// share a prefix (e.g. diagnosticSignals) are still checked for usage.
const dynamicKeyPatterns = [
  { prefix: "diagnosticSignal", suffix: "Title" },
  { prefix: "diagnosticSignal", suffix: "Detail" },
  { prefix: "diagnosticSource", suffix: "Label" },
  { prefix: "diagnosticOmitted", suffix: "" },
  { prefix: "diagnosticEvolution", suffix: "Title" },
  { prefix: "diagnosticEvolution", suffix: "Hypothesis" },
  { prefix: "diagnosticEvolution", suffix: "Experiment" },
  { prefix: "diagnosticEvolution", suffix: "Verification" },
  { prefix: "diagnosticEvolution", suffix: "Evidence" },
];

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

function listSourceFiles(dir) {
  const files = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const entryPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...listSourceFiles(entryPath));
    } else if (/\.tsx?$/.test(entry.name)) {
      files.push(entryPath);
    }
  }
  return files;
}

function isIdentifierChar(char) {
  return /[A-Za-z0-9_$]/.test(char || "");
}

// One pass over a source file: skips comments, tracks template-literal
// nesting, and collects every plain string literal plus the subset that
// appears inside the argument list of a t(...) call (ternary branches and
// nested expressions included).
function scanSource(source, literals, translateArgs) {
  const templateStack = [];
  const parenStack = [];
  let index = 0;

  const readQuoted = (quote) => {
    let value = "";
    index += 1;
    while (index < source.length) {
      const char = source[index];
      if (char === "\\") {
        value += char + (source[index + 1] || "");
        index += 2;
        continue;
      }
      if (char === quote) {
        index += 1;
        return value;
      }
      value += char;
      index += 1;
    }
    return value;
  };

  // Raw contents are kept verbatim; escape sequences never appear in copy
  // keys, so unescaped comparison is enough.
  const record = (raw) => {
    literals.add(raw);
    if (parenStack.some(Boolean)) translateArgs.add(raw);
  };

  while (index < source.length) {
    const char = source[index];
    const next = source[index + 1];
    const frame = templateStack[templateStack.length - 1];

    if (frame && frame.braceDepth === 0) {
      if (char === "\\") {
        index += 2;
        continue;
      }
      if (char === "`") {
        templateStack.pop();
        index += 1;
        continue;
      }
      if (char === "$" && next === "{") {
        frame.braceDepth = 1;
        index += 2;
        continue;
      }
      index += 1;
      continue;
    }

    if (char === "/" && next === "/") {
      while (index < source.length && source[index] !== "\n") index += 1;
      continue;
    }
    if (char === "/" && next === "*") {
      const end = source.indexOf("*/", index + 2);
      index = end < 0 ? source.length : end + 2;
      continue;
    }
    if (char === "/") {
      // Regex literal, not division or JSX `</`: only after chars that cannot
      // end an expression. Skipping it keeps quotes/parens inside regexes
      // (e.g. /["\\...]/) from desyncing the scanner.
      let cursor = index - 1;
      while (cursor >= 0 && /\s/.test(source[cursor])) cursor -= 1;
      if (cursor < 0 || "(,=:;[!".includes(source[cursor])) {
        index += 1;
        let inClass = false;
        while (index < source.length) {
          const regexChar = source[index];
          if (regexChar === "\\") {
            index += 2;
            continue;
          }
          if (regexChar === "[") inClass = true;
          else if (regexChar === "]") inClass = false;
          else if (regexChar === "/" && !inClass) {
            index += 1;
            break;
          } else if (regexChar === "\n") break;
          index += 1;
        }
        while (index < source.length && /[a-z]/i.test(source[index])) index += 1;
        continue;
      }
    }
    if (char === "\"" || char === "'") {
      record(readQuoted(char));
      continue;
    }
    if (char === "`") {
      templateStack.push({ braceDepth: 0 });
      index += 1;
      continue;
    }
    if (frame && char === "{") {
      frame.braceDepth += 1;
      index += 1;
      continue;
    }
    if (frame && char === "}") {
      frame.braceDepth -= 1;
      index += 1;
      continue;
    }
    if (char === "(") {
      let cursor = index - 1;
      while (cursor >= 0 && /\s/.test(source[cursor])) cursor -= 1;
      const isTranslateCall = source[cursor] === "t" && !isIdentifierChar(source[cursor - 1]) && source[cursor - 1] !== ".";
      parenStack.push(isTranslateCall);
      index += 1;
      continue;
    }
    if (char === ")") {
      parenStack.pop();
      index += 1;
      continue;
    }
    index += 1;
  }
}

function matchesDynamicPattern(key) {
  return dynamicKeyPatterns.some(({ prefix, suffix }) => key.startsWith(prefix) && key.endsWith(suffix) && key.length > prefix.length + suffix.length);
}

function validateCopy() {
  const source = fs.readFileSync(copySourcePath, "utf8");
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

  // literals: every plain string literal in ui/src (loose, keeps keys passed
  // through helpers such as countLabel or literal unions like t(view) alive).
  // translateArgs: literals seen inside t(...) argument lists (strict, drives
  // the missing-reference check).
  const literals = new Set();
  const translateArgs = new Set();
  for (const filePath of listSourceFiles(uiSourceDir)) {
    if (filePath === copySourcePath) continue;
    scanSource(fs.readFileSync(filePath, "utf8"), literals, translateArgs);
  }

  const missingReferences = Array.from(translateArgs).filter((key) => /^[A-Za-z][A-Za-z0-9_]*$/.test(key) && !baseline.has(key)).sort();
  if (missingReferences.length) errors.push(`Referenced copy keys missing from en: ${missingReferences.join(", ")}`);

  const unusedKeys = baselineKeys.filter((key) => !literals.has(key) && !matchesDynamicPattern(key));
  if (unusedKeys.length) errors.push(`Unused en keys (not referenced statically or via a whitelisted dynamic prefix): ${unusedKeys.join(", ")}`);

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
