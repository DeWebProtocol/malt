const BACKENDS = Object.freeze(["kzg", "ipa"]);
const BACKEND_SET = new Set(BACKENDS);

function errorMessage(error) {
  return error instanceof Error ? error.message : String(error);
}

function requireBackend(backend) {
  if (!BACKEND_SET.has(backend)) {
    throw new Error(`unsupported writer backend ${JSON.stringify(backend)}`);
  }
  return backend;
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  // A caller may intentionally use only one backend. Keep failure of the other
  // readiness Promise observable without producing an unhandled rejection.
  void promise.catch(() => {});
  return { promise, resolve, reject };
}

async function compileModule(wasmURL, fetchFunction) {
  const response = await fetchFunction(wasmURL);
  if (!response.ok) {
    throw new Error(`fetch ${wasmURL}: HTTP ${response.status}`);
  }
  if (typeof WebAssembly.compileStreaming === "function") {
    const fallback = response.clone();
    try {
      return await WebAssembly.compileStreaming(response);
    } catch {
      return WebAssembly.compile(await fallback.arrayBuffer());
    }
  }
  return WebAssembly.compile(await response.arrayBuffer());
}

function defaultWorkerFactory({ backend, workerURL }) {
  return new Worker(workerURL, {
    type: "module",
    name: `malt-writer-${backend}`,
  });
}

function validateWorker(worker) {
  if (
    !worker ||
    typeof worker.postMessage !== "function" ||
    typeof worker.addEventListener !== "function" ||
    typeof worker.terminate !== "function"
  ) {
    throw new Error("workerFactory must return a Worker-compatible object");
  }
  return worker;
}

export class MaltWriterWorkers {
  #module;
  #wasmExecURL;
  #workerURL;
  #workerFactory;
  #states = new Map();
  #nextRequestID = 1;
  #terminated = false;

  constructor({ module, wasmExecURL, workerURL, workerFactory = defaultWorkerFactory }) {
    try {
      WebAssembly.Module.exports(module);
    } catch {
      throw new Error("module must be a compiled WebAssembly.Module");
    }
    if (typeof wasmExecURL !== "string" || wasmExecURL.length === 0) {
      throw new Error("wasmExecURL must be a non-empty string");
    }
    if (typeof workerFactory !== "function") {
      throw new Error("workerFactory must be a function");
    }

    this.#module = module;
    this.#wasmExecURL = wasmExecURL;
    this.#workerURL = workerURL;
    this.#workerFactory = workerFactory;
    for (const backend of BACKENDS) {
      this.#start(backend);
    }
    this.kzgReady = this.#states.get("kzg").ready.promise;
    this.ipaReady = this.#states.get("ipa").ready.promise;
  }

  #start(backend) {
    const state = {
      backend,
      phase: "initializing",
      error: undefined,
      ready: deferred(),
      pending: new Map(),
      worker: undefined,
    };
    this.#states.set(backend, state);
    try {
      state.worker = validateWorker(
        this.#workerFactory({ backend, workerURL: this.#workerURL }),
      );
      state.worker.addEventListener("message", (event) => {
        this.#handleMessage(state, event.data);
      });
      state.worker.addEventListener("error", (event) => {
        this.#fail(state, event?.error ?? event?.message ?? "Worker error");
      });
      state.worker.addEventListener("messageerror", () => {
        this.#fail(state, "Worker message could not be deserialized");
      });
      state.worker.postMessage({
        type: "initialize",
        backend,
        module: this.#module,
        wasmExecURL: this.#wasmExecURL,
      });
    } catch (error) {
      this.#fail(state, error);
    }
  }

  #handleMessage(state, message) {
    if (!message || typeof message !== "object") {
      this.#fail(state, "Worker returned a non-object message");
      return;
    }
    if (message.backend !== undefined && message.backend !== state.backend) {
      this.#fail(
        state,
        `Worker identified itself as ${JSON.stringify(message.backend)}, expected ${JSON.stringify(state.backend)}`,
      );
      return;
    }
    if (message.type === "ready") {
      if (state.phase !== "initializing") {
        this.#fail(state, `Worker became ready from state ${state.phase}`);
        return;
      }
      state.phase = "ready";
      state.ready.resolve(Object.freeze({ backend: state.backend }));
      return;
    }
    if (message.type === "failed") {
      this.#fail(state, message.error ?? "Worker failed");
      return;
    }
    if (message.type !== "response") {
      this.#fail(state, `Worker returned unsupported message ${JSON.stringify(message.type)}`);
      return;
    }

    const pending = state.pending.get(message.id);
    if (!pending) {
      this.#fail(state, `Worker returned unknown request id ${JSON.stringify(message.id)}`);
      return;
    }
    state.pending.delete(message.id);
    if (message.error !== undefined) {
      pending.reject(new Error(String(message.error)));
    } else {
      pending.resolve(message.result);
    }
  }

  #fail(state, error) {
    if (state.phase === "failed" || state.phase === "terminated") {
      return;
    }
    const failure = error instanceof Error ? error : new Error(errorMessage(error));
    state.phase = "failed";
    state.error = failure;
    state.ready.reject(failure);
    for (const pending of state.pending.values()) {
      pending.reject(failure);
    }
    state.pending.clear();
  }

  status(backend) {
    const state = this.#states.get(requireBackend(backend));
    return Object.freeze({
      backend,
      state: state.phase,
      ...(state.error ? { error: state.error.message } : {}),
    });
  }

  whenReady(backend) {
    return this.#states.get(requireBackend(backend)).ready.promise;
  }

  async #request(backend, method, args) {
    const state = this.#states.get(requireBackend(backend));
    if (this.#terminated) {
      throw new Error("MALT writer Workers are terminated");
    }
    await state.ready.promise;
    if (state.phase !== "ready") {
      throw state.error ?? new Error(`${backend} writer is not ready`);
    }
    const id = this.#nextRequestID++;
    if (!Number.isSafeInteger(id)) {
      throw new Error("MALT writer request id space is exhausted");
    }
    return new Promise((resolve, reject) => {
      state.pending.set(id, { resolve, reject });
      try {
        state.worker.postMessage({ type: "request", id, method, args });
      } catch (error) {
        state.pending.delete(id);
        reject(error);
      }
    });
  }

  compute(backend, operationID, updateViewJSON, semanticIntentJSON) {
    return this.#request(backend, "compute", [
      operationID,
      updateViewJSON,
      semanticIntentJSON,
    ]);
  }

  load(backend, updateViewJSON) {
    return this.#request(backend, "load", [updateViewJSON]);
  }

  prepare(backend, operationID, semanticIntentJSON) {
    return this.#request(backend, "prepare", [operationID, semanticIntentJSON]);
  }

  prepareCompact(backend, operationID, semanticIntentJSON) {
    return this.#request(backend, "prepareCompact", [operationID, semanticIntentJSON]);
  }

  acceptReceipt(backend, operationID, materializationReceiptJSON) {
    return this.#request(backend, "acceptReceipt", [
      operationID,
      materializationReceiptJSON,
    ]);
  }

  discard(backend, operationID) {
    return this.#request(backend, "discard", [operationID]);
  }

  terminate() {
    if (this.#terminated) {
      return;
    }
    this.#terminated = true;
    for (const state of this.#states.values()) {
      state.worker?.terminate();
      if (state.phase !== "failed") {
        const failure = new Error(`${state.backend} writer was terminated`);
        state.error = failure;
        state.ready.reject(failure);
        for (const pending of state.pending.values()) {
          pending.reject(failure);
        }
        state.pending.clear();
        state.phase = "terminated";
      }
    }
  }
}

export async function createMaltWriterWorkers({
  wasmURL = new URL("./malt-writer.wasm", import.meta.url),
  wasmExecURL = new URL("./wasm_exec.js", import.meta.url),
  workerURL = new URL("./malt-writer-worker.mjs", import.meta.url),
  module,
  fetch: fetchFunction = globalThis.fetch,
  workerFactory,
} = {}) {
  let compiledModule = module;
  if (compiledModule === undefined) {
    if (typeof fetchFunction !== "function") {
      throw new Error("fetch is unavailable and no compiled module was provided");
    }
    compiledModule = await compileModule(wasmURL, fetchFunction);
  }
  return new MaltWriterWorkers({
    module: compiledModule,
    wasmExecURL: String(wasmExecURL),
    workerURL,
    ...(workerFactory ? { workerFactory } : {}),
  });
}
