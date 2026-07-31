const BACKENDS = new Set(["kzg", "ipa"]);
const IPA_PROFILES = new Set(["direct", "compact", "fast"]);

function errorMessage(error) {
  return error instanceof Error ? error.message : String(error);
}

function requireTarget(backend, profile = "") {
  if (!BACKENDS.has(backend)) {
    throw new Error(`unsupported writer backend ${JSON.stringify(backend)}`);
  }
  if (backend === "kzg") {
    if (profile !== "") {
      throw new Error("KZG writer must not select an IPA committer profile");
    }
  } else if (!IPA_PROFILES.has(profile)) {
    throw new Error(`unsupported IPA committer profile ${JSON.stringify(profile)}`);
  }
  return Object.freeze({ backend, profile });
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
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

function defaultWorkerFactory({ backend, profile, workerURL }) {
  const suffix = profile ? `${backend}-${profile}` : backend;
  return new Worker(workerURL, { type: "module", name: `malt-writer-${suffix}` });
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

// MaltWriterWorker owns exactly one immutable backend/profile WASM instance.
// Callers must terminate it before selecting a different implementation.
export class MaltWriterWorker {
  #target;
  #state;
  #nextRequestID = 1;

  constructor({
    backend,
    profile = "",
    module,
    wasmExecURL,
    workerURL,
    workerFactory = defaultWorkerFactory,
  }) {
    this.#target = requireTarget(backend, profile);
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

    const ready = deferred();
    this.#state = {
      phase: "initializing",
      error: undefined,
      ready,
      pending: new Map(),
      worker: undefined,
    };
    this.ready = ready.promise;
    try {
      const worker = validateWorker(workerFactory({ ...this.#target, workerURL }));
      this.#state.worker = worker;
      worker.addEventListener("message", (event) => this.#handleMessage(event.data));
      worker.addEventListener("error", (event) => {
        this.#fail(event?.error ?? event?.message ?? "Worker error");
      });
      worker.addEventListener("messageerror", () => {
        this.#fail("Worker message could not be deserialized");
      });
      worker.postMessage({
        type: "initialize",
        ...this.#target,
        module,
        wasmExecURL,
      });
    } catch (error) {
      this.#fail(error);
    }
  }

  get backend() {
    return this.#target.backend;
  }

  get profile() {
    return this.#target.profile;
  }

  #handleMessage(message) {
    if (!message || typeof message !== "object") {
      this.#fail("Worker returned a non-object message");
      return;
    }
    for (const field of ["backend", "profile"]) {
      if (message[field] !== undefined && message[field] !== this.#target[field]) {
        this.#fail(
          `Worker ${field} ${JSON.stringify(message[field])}, expected ${JSON.stringify(this.#target[field])}`,
        );
        return;
      }
    }
    if (message.type === "ready") {
      if (this.#state.phase !== "initializing") {
        this.#fail(`Worker became ready from state ${this.#state.phase}`);
        return;
      }
      this.#state.phase = "ready";
      this.#state.ready.resolve(this.#target);
      return;
    }
    if (message.type === "failed") {
      this.#fail(message.error ?? "Worker failed");
      return;
    }
    if (message.type !== "response") {
      this.#fail(`Worker returned unsupported message ${JSON.stringify(message.type)}`);
      return;
    }

    const pending = this.#state.pending.get(message.id);
    if (!pending) {
      this.#fail(`Worker returned unknown request id ${JSON.stringify(message.id)}`);
      return;
    }
    this.#state.pending.delete(message.id);
    if (message.error !== undefined) {
      pending.reject(new Error(String(message.error)));
    } else {
      pending.resolve(message.result);
    }
  }

  #fail(error) {
    if (this.#state.phase === "failed" || this.#state.phase === "terminated") return;
    const failure = error instanceof Error ? error : new Error(errorMessage(error));
    this.#stop("failed", failure);
  }

  #stop(phase, failure) {
    if (this.#state.phase === "failed" || this.#state.phase === "terminated") return;
    this.#state.phase = phase;
    this.#state.error = failure;
    const worker = this.#state.worker;
    this.#state.worker = undefined;
    try {
      worker?.terminate();
    } catch {
      // Readiness and pending requests are rejected even if a custom adapter
      // fails while releasing its underlying Worker.
    }
    this.#state.ready.reject(failure);
    for (const pending of this.#state.pending.values()) pending.reject(failure);
    this.#state.pending.clear();
  }

  #requireBackend(backend) {
    if (backend !== this.backend) {
      throw new Error(
        `loaded ${this.backend} writer cannot serve ${JSON.stringify(backend)}`,
      );
    }
  }

  status(backend = this.backend) {
    this.#requireBackend(backend);
    return Object.freeze({
      ...this.#target,
      state: this.#state.phase,
      ...(this.#state.error ? { error: this.#state.error.message } : {}),
    });
  }

  whenReady(backend = this.backend) {
    this.#requireBackend(backend);
    return this.#state.ready.promise;
  }

  async #request(backend, method, args) {
    this.#requireBackend(backend);
    await this.#state.ready.promise;
    if (this.#state.phase !== "ready") {
      throw this.#state.error ?? new Error(`${backend} writer is not ready`);
    }
    const id = this.#nextRequestID++;
    if (!Number.isSafeInteger(id)) {
      throw new Error("MALT writer request id space is exhausted");
    }
    return new Promise((resolve, reject) => {
      this.#state.pending.set(id, { resolve, reject });
      try {
        this.#state.worker.postMessage({ type: "request", id, method, args });
      } catch (error) {
        this.#state.pending.delete(id);
        reject(error);
      }
    });
  }

  compute(backend, operationID, updateViewJSON, semanticIntentJSON) {
    return this.#request(backend, "compute", [operationID, updateViewJSON, semanticIntentJSON]);
  }
  bootstrap(backend) { return this.#request(backend, "bootstrap", []); }
  load(backend, updateViewJSON) { return this.#request(backend, "load", [updateViewJSON]); }
  prepare(backend, operationID, semanticIntentJSON) {
    return this.#request(backend, "prepare", [operationID, semanticIntentJSON]);
  }
  getPreparedResult(backend, operationID) {
    return this.#request(backend, "getPreparedResult", [operationID]);
  }
  validateReceipt(backend, writerResultJSON, materializationReceiptJSON) {
    return this.#request(backend, "validateReceipt", [writerResultJSON, materializationReceiptJSON]);
  }
  acceptReceipt(backend, operationID, materializationReceiptJSON) {
    return this.#request(backend, "acceptReceipt", [operationID, materializationReceiptJSON]);
  }
  discard(backend, operationID) { return this.#request(backend, "discard", [operationID]); }
  closeSession(backend) {
    return this.#request(backend, "closeSession", []).then(() => undefined);
  }

  terminateBackend(backend) {
    this.#requireBackend(backend);
    this.terminate();
  }

  terminateAll() { this.terminate(); }

  terminate() {
    this.#stop("terminated", new Error(`${this.backend} writer was terminated`));
  }
}

export async function createMaltWriterWorker({
  backend,
  profile = "",
  wasmURL,
  wasmExecURL = new URL("./wasm_exec.js", import.meta.url),
  workerURL = new URL("./malt-writer-worker.mjs", import.meta.url),
  module,
  fetch: fetchFunction = globalThis.fetch,
  workerFactory,
} = {}) {
  requireTarget(backend, profile);
  let compiledModule = module;
  if (compiledModule === undefined) {
    if (wasmURL === undefined) throw new Error("wasmURL is required");
    if (typeof fetchFunction !== "function") {
      throw new Error("fetch is unavailable and no compiled module was provided");
    }
    compiledModule = await compileModule(wasmURL, fetchFunction);
  }
  return new MaltWriterWorker({
    backend,
    profile,
    module: compiledModule,
    wasmExecURL: String(wasmExecURL),
    workerURL,
    ...(workerFactory ? { workerFactory } : {}),
  });
}
