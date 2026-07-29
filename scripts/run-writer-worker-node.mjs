import { webcrypto } from "node:crypto";
import { parentPort, workerData } from "node:worker_threads";

if (!parentPort) {
  throw new Error("run-writer-worker-node.mjs must run in a Node worker thread");
}
if (!globalThis.crypto) {
  globalThis.crypto = webcrypto;
}

const listeners = new Map();
const queuedMessages = [];

globalThis.self = globalThis;
globalThis.postMessage = (message, transfer) => {
  parentPort.postMessage(message, transfer);
};
globalThis.addEventListener = (type, listener) => {
  if (typeof listener !== "function") {
    throw new TypeError("event listener must be a function");
  }
  const typedListeners = listeners.get(type) ?? [];
  typedListeners.push(listener);
  listeners.set(type, typedListeners);
  if (type === "message" && queuedMessages.length > 0) {
    for (const data of queuedMessages.splice(0)) {
      listener({ data });
    }
  }
};

parentPort.on("message", (data) => {
  const messageListeners = listeners.get("message");
  if (!messageListeners || messageListeners.length === 0) {
    queuedMessages.push(data);
    return;
  }
  for (const listener of messageListeners) {
    listener({ data });
  }
});

await import(workerData.workerURL);
