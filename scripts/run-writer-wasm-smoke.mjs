import assert from "node:assert/strict";
import { webcrypto } from "node:crypto";
import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

const [wasmPath, wasmExecPath, fixturePath, selectedBackend = "all"] =
  process.argv.slice(2);
if (!wasmPath || !wasmExecPath || !fixturePath) {
  console.error(
    "usage: node run-writer-wasm-smoke.mjs <writer.wasm> <wasm_exec.js> <fixtures.json> [all|kzg|ipa]",
  );
  process.exit(2);
}
if (!["all", "kzg", "ipa"].includes(selectedBackend)) {
  throw new Error(`unsupported writer backend ${JSON.stringify(selectedBackend)}`);
}
if (!globalThis.crypto) {
  globalThis.crypto = webcrypto;
}
const fixtures = JSON.parse(await readFile(fixturePath, "utf8"));

await import(pathToFileURL(wasmExecPath).href);
if (typeof globalThis.Go !== "function") {
  throw new Error(`${wasmExecPath} did not install the Go WASM runtime`);
}

globalThis.maltWriterBackend = selectedBackend;
const go = new globalThis.Go();
const wasm = await readFile(wasmPath);
const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
let runtimeFailure;
void go.run(instance).catch((error) => {
  runtimeFailure = error;
});

const deadline = Date.now() + 120_000;
while (Date.now() < deadline) {
  if (runtimeFailure) {
    throw new Error(`Go WASM runtime failed: ${runtimeFailure}`);
  }
  if (globalThis.maltWriterReady && typeof globalThis.maltComputeClientRootV1 === "function") {
    break;
  }
  await new Promise((resolve) => setTimeout(resolve, 10));
}
if (!globalThis.maltWriterReady || typeof globalThis.maltComputeClientRootV1 !== "function") {
  throw new Error("timed out waiting for MALT writer globals");
}
if (globalThis.maltWriterInitError) {
  throw new Error(`MALT writer initialization failed: ${globalThis.maltWriterInitError}`);
}
if (globalThis.maltWriterLoadedBackend !== selectedBackend) {
  throw new Error(
    `MALT writer loaded backend ${JSON.stringify(globalThis.maltWriterLoadedBackend)}, expected ${JSON.stringify(selectedBackend)}`,
  );
}

let rejectedStrings = false;
try {
  await globalThis.maltComputeClientRootV1("smoke", "{}", "{}");
} catch {
  rejectedStrings = true;
}
if (!rejectedStrings) {
  throw new Error("writer accepted JSON strings instead of bounded Uint8Arrays");
}

const encoder = new TextEncoder();
const hostileLength = new Proxy(encoder.encode("smoke"), {
  get(target, property) {
    if (property === "byteLength") {
      return -1;
    }
    return Reflect.get(target, property, target);
  },
});
let rejectedHostileLength = false;
try {
  await globalThis.maltComputeClientRootV1(
    hostileLength,
    encoder.encode("{}"),
    encoder.encode("{}"),
  );
} catch {
  rejectedHostileLength = true;
}
if (!rejectedHostileLength) {
  throw new Error("writer accepted a proxied Uint8Array with hostile length metadata");
}

if (selectedBackend === "all") {
  const oversizedJSON = new Uint8Array(64 * 1024 * 1024 + 1);
  let rejectedOversized = false;
  try {
    await globalThis.maltComputeClientRootV1(
      encoder.encode("smoke"),
      oversizedJSON,
      encoder.encode("{}"),
    );
  } catch {
    rejectedOversized = true;
  }
  if (!rejectedOversized) {
    throw new Error("writer accepted JSON above the 64 MiB wire limit");
  }
}

let rejectedInvalidJSON = false;
try {
  await globalThis.maltComputeClientRootV1(
    encoder.encode("smoke"),
    encoder.encode("{}"),
    encoder.encode("{}"),
  );
} catch {
  rejectedInvalidJSON = true;
}
if (!rejectedInvalidJSON) {
  throw new Error("writer accepted invalid update-view and semantic-intent JSON");
}

const selectedFixtures = fixtures.filter(
  ({ backend }) => selectedBackend === "all" || backend === selectedBackend,
);
const expectedFixtureCount = selectedBackend === "all" ? 2 : 1;
if (selectedFixtures.length !== expectedFixtureCount) {
  throw new Error(
    `selected ${selectedFixtures.length} fixtures for ${selectedBackend}, expected ${expectedFixtureCount}`,
  );
}
for (const fixture of selectedFixtures) {
  const operationID = encoder.encode(fixture.operation_id);
  Object.defineProperty(operationID, "byteLength", { value: -1 });
  const updateView = encoder.encode(JSON.stringify(fixture.update_view));
  const semanticIntent = encoder.encode(JSON.stringify(fixture.semantic_intent));
  for (const input of [operationID, updateView, semanticIntent]) {
    Object.defineProperty(input, "subarray", {
      value() {
        throw new Error("caller-controlled subarray must not be invoked");
      },
    });
  }
  const resultJSON = await globalThis.maltComputeClientRootV1(
    operationID,
    updateView,
    semanticIntent,
  );
  const result = JSON.parse(resultJSON);
  if (result.profile !== "malt.writer-compute-result/v1") {
    throw new Error(`unexpected writer result profile ${JSON.stringify(result.profile)}`);
  }
  if (result.bundle.operation_id !== fixture.operation_id) {
    throw new Error(`unexpected operation ID for ${fixture.backend}`);
  }
  assert.deepStrictEqual(
    result.bundle,
    fixture.expected_bundle,
    `${fixture.backend} WASM bundle differs from the native canonical bundle`,
  );
  assert.deepStrictEqual(
    result.next_view,
    fixture.expected_next_view,
    `${fixture.backend} WASM next view differs from the native canonical next view`,
  );
  assert.deepStrictEqual(
    Object.keys(result.metrics).sort(),
    [
      "bundle_validation_ns",
      "commitment_update_ns",
      "digest_ns",
      "expected_root_encoding_ns",
      "intent_normalization_ns",
      "next_view_ns",
      "root_computation_ns",
      "total_ns",
      "view_normalization_ns",
    ],
    `${fixture.backend} result has an unexpected metrics shape`,
  );
  for (const [name, value] of Object.entries(result.metrics)) {
    if (!Number.isSafeInteger(value) || value < 0) {
      throw new Error(`${fixture.backend} metric ${name} is not a non-negative safe integer`);
    }
  }
}

console.log(
  `WASM ${selectedBackend} writer smoke passed (${selectedFixtures.length} valid fixture(s))`,
);
process.exit(0);
