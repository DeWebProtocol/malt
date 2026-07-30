import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { Worker as NodeWorker } from "node:worker_threads";
import { pathToFileURL } from "node:url";

const [wasmPath, wasmExecPath, controllerPath, workerPath, fixturePath] =
  process.argv.slice(2);
if (!wasmPath || !wasmExecPath || !controllerPath || !workerPath || !fixturePath) {
  console.error(
    "usage: node run-writer-dual-worker-smoke.mjs <writer.wasm> <wasm_exec.js> <controller.mjs> <worker.mjs> <fixtures.json>",
  );
  process.exit(2);
}

const [{ createMaltWriterWorkers }, wasm, fixtureJSON] = await Promise.all([
  import(pathToFileURL(controllerPath).href),
  readFile(wasmPath),
  readFile(fixturePath, "utf8"),
]);
const module = await WebAssembly.compile(wasm);
const fixtures = JSON.parse(fixtureJSON);
const nodeWorkerWrapper = new URL("./run-writer-worker-node.mjs", import.meta.url);
const workerThreads = [];

class NodeWorkerAdapter {
  constructor(workerURL) {
    this.worker = new NodeWorker(nodeWorkerWrapper, {
      workerData: { workerURL: String(workerURL) },
    });
    workerThreads.push(this.worker);
  }

  addEventListener(type, listener) {
    if (type === "message") {
      this.worker.on("message", (data) => listener({ data }));
      return;
    }
    if (type === "error") {
      this.worker.on("error", (error) => listener({ error, message: error.message }));
      return;
    }
    if (type === "messageerror") {
      this.worker.on("messageerror", (error) => listener({ error }));
    }
  }

  postMessage(message) {
    this.worker.postMessage(message);
  }

  terminate() {
    return this.worker.terminate();
  }
}

const startedAt = performance.now();
const writers = await createMaltWriterWorkers({
  module,
  wasmExecURL: pathToFileURL(wasmExecPath),
  workerURL: pathToFileURL(workerPath),
  workerFactory: ({ workerURL }) => new NodeWorkerAdapter(workerURL),
});

try {
  assert.equal(writers.status("kzg").state, "initializing");
  assert.equal(writers.status("ipa").state, "initializing");
  assert.equal(workerThreads.length, 2);
  assert.notEqual(workerThreads[0].threadId, workerThreads[1].threadId);

  await writers.kzgReady;
  const kzgReadyAt = performance.now();
  assert.equal(
    writers.status("ipa").state,
    "initializing",
    "IPA became ready before KZG, so the smoke test did not exercise independent readiness",
  );

  const encoder = new TextEncoder();
  const kzgFixture = fixtures.find(({ backend }) => backend === "kzg");
  assert.ok(kzgFixture, "missing KZG fixture");
  const loadedRoot = await writers.load(
    "kzg",
    encoder.encode(JSON.stringify(kzgFixture.update_view)),
  );
  assert.equal(loadedRoot, kzgFixture.update_view.base_root);
  const kzgCandidate = await writers.prepare(
    "kzg",
    encoder.encode(kzgFixture.operation_id),
    encoder.encode(JSON.stringify(kzgFixture.semantic_intent)),
  );
  assert.equal(kzgCandidate, kzgFixture.expected_bundle.candidate);
  const kzgPreparedJSON = await writers.getPreparedResult(
    "kzg",
    encoder.encode(kzgFixture.operation_id),
  );
  const kzgPrepared = JSON.parse(kzgPreparedJSON);
  assert.equal(kzgPrepared.profile, "malt.writer-compute-result/v1");
  assert.deepStrictEqual(kzgPrepared.bundle, kzgFixture.expected_bundle);
  assert.deepStrictEqual(kzgPrepared.next_view, kzgFixture.expected_next_view);
  const kzgWorkFinishedAt = performance.now();
  assert.equal(
    writers.status("ipa").state,
    "initializing",
    "KZG work did not finish while IPA initialization was still in progress",
  );

  await writers.ipaReady;
  const ipaReadyAt = performance.now();
  const ipaFixture = fixtures.find(({ backend }) => backend === "ipa");
  assert.ok(ipaFixture, "missing IPA fixture");
  const ipaLoadedRoot = await writers.load(
    "ipa",
    encoder.encode(JSON.stringify(ipaFixture.update_view)),
  );
  assert.equal(ipaLoadedRoot, ipaFixture.update_view.base_root);
  const ipaCandidate = await writers.prepare(
    "ipa",
    encoder.encode(ipaFixture.operation_id),
    encoder.encode(JSON.stringify(ipaFixture.semantic_intent)),
  );
  assert.equal(ipaCandidate, ipaFixture.expected_bundle.candidate);
  const ipaPreparedJSON = await writers.getPreparedResult(
    "ipa",
    encoder.encode(ipaFixture.operation_id),
  );
  const ipaPrepared = JSON.parse(ipaPreparedJSON);
  assert.equal(ipaPrepared.profile, "malt.writer-compute-result/v1");
  assert.deepStrictEqual(ipaPrepared.bundle, ipaFixture.expected_bundle);
  assert.deepStrictEqual(ipaPrepared.next_view, ipaFixture.expected_next_view);

  const orderedReload = writers.load(
    "kzg",
    encoder.encode(JSON.stringify(kzgFixture.update_view)),
  );
  const orderedClose = writers.closeSession("kzg");
  assert.equal(await orderedReload, kzgFixture.update_view.base_root);
  assert.equal(await orderedClose, undefined);
  await assert.rejects(
    writers.prepare(
      "kzg",
      encoder.encode(`${kzgFixture.operation_id}-after-close`),
      encoder.encode(JSON.stringify(kzgFixture.semantic_intent)),
    ),
    /has no update view/,
    "stateful Worker RPCs did not preserve load-then-close arrival order",
  );
  assert.equal(writers.status("kzg").state, "ready");
  assert.equal(writers.status("ipa").state, "ready");
  assert.equal(
    await writers.getPreparedResult("ipa", encoder.encode(ipaFixture.operation_id)),
    ipaPreparedJSON,
    "closing the KZG session affected the IPA prepared candidate",
  );

  console.log(
    [
      "dual Worker smoke passed",
      `KZG ready ${(kzgReadyAt - startedAt).toFixed(1)} ms`,
      `KZG work finished ${(kzgWorkFinishedAt - startedAt).toFixed(1)} ms`,
      `IPA ready ${(ipaReadyAt - startedAt).toFixed(1)} ms`,
      `threads ${workerThreads.map(({ threadId }) => threadId).join(",")}`,
    ].join("; "),
  );
} finally {
  writers.terminateAll();
}
