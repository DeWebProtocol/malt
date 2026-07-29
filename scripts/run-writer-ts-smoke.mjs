import assert from "node:assert/strict";
import { webcrypto } from "node:crypto";
import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

const [wasmPath, wasmExecPath, sessionPath, fixturePath, selectedBackend] =
  process.argv.slice(2);
if (
  !wasmPath || !wasmExecPath || !sessionPath || !fixturePath ||
  !["kzg", "ipa"].includes(selectedBackend)
) {
  console.error(
    "usage: node run-writer-ts-smoke.mjs <commitment.wasm> <wasm_exec.js> <writer-session.ts> <fixtures.json> <kzg|ipa>",
  );
  process.exit(2);
}
if (!globalThis.crypto) {
  globalThis.crypto = webcrypto;
}
const fixtures = JSON.parse(await readFile(fixturePath, "utf8"));
const selectedFixtures = fixtures.filter(({ backend }) => backend === selectedBackend);
if (selectedFixtures.length !== 4) {
  throw new Error(
    `fixture contains ${selectedFixtures.length} ${selectedBackend} cases, expected 4`,
  );
}

await import(pathToFileURL(wasmExecPath).href);
if (typeof globalThis.Go !== "function") {
  throw new Error(`${wasmExecPath} did not install the Go WASM runtime`);
}
const go = new globalThis.Go();
const wasm = await readFile(wasmPath);
const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
let runtimeFailure;
void go.run(instance).catch((error) => {
  runtimeFailure = error;
});
await waitForCommitmentReady(() => runtimeFailure);
if (globalThis.maltCommitmentInitError) {
  throw new Error(`commitment initialization failed: ${globalThis.maltCommitmentInitError}`);
}
if (globalThis.maltCommitmentLoadedBackend !== selectedBackend) {
  throw new Error(
    `loaded ${JSON.stringify(globalThis.maltCommitmentLoadedBackend)}, expected ${JSON.stringify(selectedBackend)}`,
  );
}
if (typeof globalThis.maltComputeClientRootV1 === "function") {
  throw new Error("commitment-only artifact unexpectedly exposes the full Go writer");
}

let rejectedStrings = false;
try {
  await globalThis.maltCommitmentLoadObjectsV1("{}");
} catch {
  rejectedStrings = true;
}
if (!rejectedStrings) {
  throw new Error("commitment backend accepted a string instead of a Uint8Array");
}
const hostile = new Proxy(new TextEncoder().encode("{}"), {
  get(target, property) {
    if (property === "byteLength") return -1;
    return Reflect.get(target, property, target);
  },
});
let rejectedHostile = false;
try {
  await globalThis.maltCommitmentLoadObjectsV1(hostile);
} catch {
  rejectedHostile = true;
}
if (!rejectedHostile) {
  throw new Error("commitment backend accepted hostile TypedArray metadata");
}

const { MaltWriterTSSession } = await import(pathToFileURL(sessionPath).href);
const neverLoaded = new MaltWriterTSSession(selectedBackend);
await neverLoaded.close();
await neverLoaded.close();
const isolationFirst = new MaltWriterTSSession(selectedBackend);
const isolationSecond = new MaltWriterTSSession(selectedBackend);
await isolationFirst.load(selectedFixtures[0].update_view);
await isolationSecond.load(selectedFixtures[1].update_view);
const isolatedPrepared = JSON.parse(await isolationFirst.prepare(
  `${selectedFixtures[0].operation_id}-isolated`,
  selectedFixtures[0].semantic_intent,
));
assert.equal(isolatedPrepared.bundle.candidate, selectedFixtures[0].expected_bundle.candidate);
await isolationFirst.discard(`${selectedFixtures[0].operation_id}-isolated`);
await isolationFirst.close();
await isolationSecond.close();

for (const fixture of selectedFixtures) {
  const session = new MaltWriterTSSession(selectedBackend);
  const inputView = structuredClone(fixture.update_view);
  const inputIntent = structuredClone(fixture.semantic_intent);
  if (fixture.case === "fixed-list-u64") {
    for (const object of inputView.objects) {
      object.commit.total_size = "9007199254740993";
      object.commit.chunk_size = 9007199254740993n;
    }
    for (const transition of inputIntent.transitions) {
      transition.commit.total_size = 9007199254740993n;
      transition.commit.chunk_size = "9007199254740993";
    }
  }
  const loaded = await session.load(inputView);
  if (loaded !== fixture.update_view.base_root) {
    throw new Error(`loaded ${loaded}, expected ${fixture.update_view.base_root}`);
  }
  if (fixture.case === "nested-map") {
    inputIntent.transitions.reverse();
  }
  if (fixture.case === "list-replace-append") {
    inputIntent.transitions[0].changes.reverse();
  }
  const preparedJSON = await session.prepare(fixture.operation_id, inputIntent);
  const prepared = JSON.parse(preparedJSON);
  assert.deepStrictEqual(
    prepared.bundle,
    fixture.expected_bundle,
    `${selectedBackend}/${fixture.case} TypeScript bundle differs from the canonical Go writer`,
  );
  assert.deepStrictEqual(
    prepared.next_view,
    fixture.expected_next_view,
    `${selectedBackend}/${fixture.case} TypeScript next view differs from the canonical Go writer`,
  );
  assert.deepStrictEqual(
    Object.keys(prepared.metrics).sort(),
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
  );
  for (const [name, value] of Object.entries(prepared.metrics)) {
    if (!Number.isSafeInteger(value) || value < 0) {
      throw new Error(
        `${selectedBackend}/${fixture.case} metric ${name} is not a non-negative safe integer`,
      );
    }
  }

  const badReceipt = { ...fixture.expected_receipt, candidate: fixture.update_view.base_root };
  let rejectedBadReceipt = false;
  try {
    await session.acceptReceipt(fixture.operation_id, badReceipt);
  } catch {
    rejectedBadReceipt = true;
  }
  if (!rejectedBadReceipt) {
    throw new Error(
      `${selectedBackend}/${fixture.case} TypeScript session accepted a mismatched receipt`,
    );
  }
  const accepted = await session.acceptReceipt(
    fixture.operation_id,
    fixture.expected_receipt,
  );
  if (accepted !== fixture.expected_bundle.candidate) {
    throw new Error(`accepted ${accepted}, expected ${fixture.expected_bundle.candidate}`);
  }
  let rejectedStale = false;
  try {
    await session.prepare(`${fixture.operation_id}-stale`, fixture.semantic_intent);
  } catch {
    rejectedStale = true;
  }
  if (!rejectedStale) {
    throw new Error(
      `${selectedBackend}/${fixture.case} TypeScript session accepted a stale intent`,
    );
  }
  await session.close();
}

console.log(
  `TypeScript + ${selectedBackend} commitment WASM smoke passed (${selectedFixtures.length} cases)`,
);
process.exit(0);

async function waitForCommitmentReady(runtimeFailure) {
  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline) {
    const failure = runtimeFailure();
    if (failure) throw new Error(`Go WASM runtime failed: ${failure}`);
    if (
      globalThis.maltCommitmentReady &&
      typeof globalThis.maltCommitmentLoadObjectsV1 === "function" &&
      typeof globalThis.maltCommitmentApplyDeltaV1 === "function" &&
      typeof globalThis.maltCommitmentRetainRootsV1 === "function" &&
      typeof globalThis.maltCommitmentDropSessionV1 === "function"
    ) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error("timed out waiting for MALT commitment globals");
}
