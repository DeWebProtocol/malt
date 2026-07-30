import assert from "node:assert/strict";
import test from "node:test";

import { MaltWriterWorkers } from "./malt-writer-workers.mjs";

const EMPTY_WASM_MODULE = new WebAssembly.Module(
  new Uint8Array([0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00]),
);

class FakeWorker {
  constructor(backend, throwOnInitialize = false) {
    this.backend = backend;
    this.throwOnInitialize = throwOnInitialize;
    this.listeners = new Map();
    this.requests = [];
    this.requestWaiters = [];
    this.terminationCount = 0;
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  postMessage(message) {
    if (message.type === "initialize") {
      if (this.throwOnInitialize) {
        throw new Error(`${this.backend} initialize postMessage failed`);
      }
      this.initialization = message;
      return;
    }
    const waiter = this.requestWaiters.shift();
    if (waiter) {
      waiter(message);
    } else {
      this.requests.push(message);
    }
  }

  terminate() {
    this.terminationCount += 1;
  }

  emit(type, event) {
    for (const listener of this.listeners.get(type) ?? []) {
      listener(event);
    }
  }

  ready() {
    this.emit("message", {
      data: { type: "ready", backend: this.backend },
    });
  }

  fail(error) {
    this.emit("error", { error, message: error.message });
  }

  nextRequest() {
    const request = this.requests.shift();
    if (request) {
      return Promise.resolve(request);
    }
    return new Promise((resolve) => {
      this.requestWaiters.push(resolve);
    });
  }

  respond(request, { result, error } = {}) {
    this.emit("message", {
      data: {
        type: "response",
        backend: this.backend,
        id: request.id,
        ...(error === undefined ? { result } : { error }),
      },
    });
  }
}

function newHarness({ throwOnInitializeBackend } = {}) {
  const workers = new Map();
  const writers = new MaltWriterWorkers({
    module: EMPTY_WASM_MODULE,
    wasmExecURL: "https://example.test/wasm_exec.js",
    workerURL: new URL("https://example.test/malt-writer-worker.mjs"),
    workerFactory: ({ backend }) => {
      const worker = new FakeWorker(backend, throwOnInitializeBackend === backend);
      workers.set(backend, worker);
      return worker;
    },
  });
  return { writers, workers };
}

function makeReady(writers, workers) {
  workers.get("kzg").ready();
  workers.get("ipa").ready();
  return Promise.all([writers.kzgReady, writers.ipaReady]);
}

test("initialize postMessage failure terminates only the failed backend", async () => {
  const { writers, workers } = newHarness({ throwOnInitializeBackend: "kzg" });
  const kzg = workers.get("kzg");
  const ipa = workers.get("ipa");

  await assert.rejects(writers.kzgReady, /kzg initialize postMessage failed/);
  assert.deepEqual(writers.status("kzg"), {
    backend: "kzg",
    state: "failed",
    error: "kzg initialize postMessage failed",
  });
  assert.equal(kzg.terminationCount, 1);
  assert.equal(ipa.terminationCount, 0);

  ipa.ready();
  await writers.ipaReady;
  assert.equal(writers.status("ipa").state, "ready");
  assert.equal(ipa.terminationCount, 0);

  writers.terminateAll();
  assert.equal(kzg.terminationCount, 1);
  assert.equal(ipa.terminationCount, 1);
});

test("request errors and session close keep the backend Worker alive", async () => {
  const { writers, workers } = newHarness();
  await makeReady(writers, workers);
  const kzg = workers.get("kzg");
  const ipa = workers.get("ipa");

  const invalidLoad = writers.load("kzg", new Uint8Array([1]));
  const invalidRequest = await kzg.nextRequest();
  kzg.respond(invalidRequest, { error: "invalid update view" });
  await assert.rejects(invalidLoad, /invalid update view/);
  assert.equal(writers.status("kzg").state, "ready");
  assert.equal(kzg.terminationCount, 0);

  const close = writers.closeSession("kzg");
  const closeRequest = await kzg.nextRequest();
  assert.equal(closeRequest.method, "closeSession");
  assert.deepEqual(closeRequest.args, []);
  kzg.respond(closeRequest, { result: "closed" });
  assert.equal(await close, undefined);
  assert.equal(writers.status("kzg").state, "ready");
  assert.equal(kzg.terminationCount, 0);
  assert.equal(ipa.terminationCount, 0);

  writers.terminateAll();
  assert.equal(kzg.terminationCount, 1);
  assert.equal(ipa.terminationCount, 1);
});

test("a fatal backend failure terminates only that Worker", async () => {
  const { writers, workers } = newHarness();
  await makeReady(writers, workers);
  const kzg = workers.get("kzg");
  const ipa = workers.get("ipa");

  const pending = writers.load("kzg", new Uint8Array([1]));
  await kzg.nextRequest();
  kzg.fail(new Error("KZG runtime crashed"));
  await assert.rejects(pending, /KZG runtime crashed/);
  assert.deepEqual(writers.status("kzg"), {
    backend: "kzg",
    state: "failed",
    error: "KZG runtime crashed",
  });
  assert.equal(kzg.terminationCount, 1);
  assert.equal(ipa.terminationCount, 0);

  kzg.fail(new Error("duplicate failure"));
  assert.equal(kzg.terminationCount, 1);

  const healthyRequest = writers.load("ipa", new Uint8Array([2]));
  const request = await ipa.nextRequest();
  ipa.respond(request, { result: "ipa-root" });
  assert.equal(await healthyRequest, "ipa-root");
  assert.equal(writers.status("ipa").state, "ready");

  writers.terminateAll();
  writers.terminate();
  assert.equal(kzg.terminationCount, 1);
  assert.equal(ipa.terminationCount, 1);
  assert.equal(writers.status("kzg").state, "failed");
  assert.equal(writers.status("ipa").state, "terminated");
});

test("terminateBackend rejects its pending work and preserves the peer", async () => {
  const { writers, workers } = newHarness();
  await makeReady(writers, workers);
  const kzg = workers.get("kzg");
  const ipa = workers.get("ipa");

  const pending = writers.prepare("kzg", new Uint8Array([1]), new Uint8Array([2]));
  await kzg.nextRequest();
  writers.terminateBackend("kzg");
  await assert.rejects(pending, /kzg writer was terminated/);
  assert.equal(writers.status("kzg").state, "terminated");
  assert.equal(kzg.terminationCount, 1);
  await assert.rejects(
    writers.load("kzg", new Uint8Array([3])),
    /kzg writer was terminated/,
  );

  const peerClose = writers.closeSession("ipa");
  const closeRequest = await ipa.nextRequest();
  ipa.respond(closeRequest, { result: undefined });
  await peerClose;
  assert.equal(writers.status("ipa").state, "ready");
  assert.equal(ipa.terminationCount, 0);

  writers.terminateBackend("kzg");
  assert.equal(kzg.terminationCount, 1);
  writers.terminate();
  assert.equal(kzg.terminationCount, 1);
  assert.equal(ipa.terminationCount, 1);
});
