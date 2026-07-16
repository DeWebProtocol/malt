import { webcrypto } from "node:crypto";
import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

const [wasmPath, wasmExecPath, corpusPath] = process.argv.slice(2);
if (!wasmPath || !wasmExecPath || !corpusPath) {
  console.error(
    "usage: node run-verifier-wasm-vectors.mjs <verifier.wasm> <wasm_exec.js> <vectors.json>",
  );
  process.exit(2);
}

if (!globalThis.crypto) {
  globalThis.crypto = webcrypto;
}

await import(pathToFileURL(wasmExecPath).href);
if (typeof globalThis.Go !== "function") {
  throw new Error(`${wasmExecPath} did not install the Go WASM runtime`);
}

const corpus = JSON.parse(await readFile(corpusPath, "utf8"));
if (!Array.isArray(corpus.vectors) || corpus.vectors.length === 0) {
  throw new Error(`${corpusPath} does not contain a non-empty vectors array`);
}

const go = new globalThis.Go();
const wasm = await readFile(wasmPath);
const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
let runtimeFailure;
void go.run(instance).catch((error) => {
  runtimeFailure = error;
});

await waitForVerifierGlobals();

const seen = new Set();
const failures = [];
for (const vector of corpus.vectors) {
  const label = typeof vector.id === "string" ? vector.id : "<missing-id>";
  try {
    validateEnvelope(vector, seen);
    const verify = selectVerifier(vector.operation);
    const response = JSON.parse(verify(JSON.stringify(vector.verification)));
    if (typeof response.valid !== "boolean") {
      throw new Error("verifier response has no boolean valid field");
    }
    if (response.valid !== vector.expected.valid) {
      const diagnostic = response.error ? `: ${response.error}` : "";
      throw new Error(
        `expected valid=${vector.expected.valid}, got valid=${response.valid}${diagnostic}`,
      );
    }
  } catch (error) {
    failures.push(`${label}: ${error instanceof Error ? error.message : error}`);
  }
}

if (failures.length > 0) {
  console.error(`WASM conformance failed (${failures.length}/${corpus.vectors.length}):`);
  for (const failure of failures) {
    console.error(`- ${failure}`);
  }
  process.exit(1);
}

console.log(`WASM conformance passed (${corpus.vectors.length} vectors)`);
process.exit(0);

function selectVerifier(operation) {
  switch (operation) {
    case "resolve":
      return globalThis.maltVerifyResolve;
    case "read":
      return globalThis.maltVerifyRead;
    default:
      throw new Error(`unsupported operation ${JSON.stringify(operation)}`);
  }
}

function validateEnvelope(vector, ids) {
  if (!vector || typeof vector !== "object" || Array.isArray(vector)) {
    throw new Error("vector is not an object");
  }
  if (typeof vector.id !== "string" || vector.id.length === 0) {
    throw new Error("vector has no non-empty id");
  }
  if (ids.has(vector.id)) {
    throw new Error("duplicate vector id");
  }
  ids.add(vector.id);
  if (!vector.verification || typeof vector.verification !== "object" || Array.isArray(vector.verification)) {
    throw new Error("verification is not an object");
  }
  if (!vector.expected || typeof vector.expected.valid !== "boolean") {
    throw new Error("expected.valid is not a boolean");
  }
}

async function waitForVerifierGlobals() {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    if (runtimeFailure) {
      throw new Error(`Go WASM runtime failed: ${runtimeFailure}`);
    }
    if (
      typeof globalThis.maltVerifyResolve === "function" &&
      typeof globalThis.maltVerifyRead === "function"
    ) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error("timed out waiting for MALT verifier globals");
}
