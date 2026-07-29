const UPDATE_VIEW_PROFILE = "malt.update-view/v1";
const STATE_PROFILE = "stateful-complete-vectors-v1";
const INTENT_PROFILE = "malt.semantic-intent/v1";
const BUNDLE_PROFILE = "malt.client-root-bundle/v1";
const RECEIPT_PROFILE = "malt.materialization-receipt/v1";
const RESULT_PROFILE = "malt.writer-compute-result/v1";
const OBJECTS_PROFILE = "malt.commitment-objects/v1";
const DELTA_PROFILE = "malt.commitment-delta/v1";
const RETAIN_PROFILE = "malt.commitment-retain/v1";
const COMMITMENT_RESULT_PROFILE = "malt.commitment-result/v1";
const MAX_OBJECTS = 1 << 16;
const MAX_ENTRIES = 1 << 20;
const MAX_DEPTH = 1 << 16;
const MAX_TRANSITIONS = 1 << 16;
const MAX_CHANGES = 1 << 20;
const MAX_PREPARED = 64;
const MAX_PREPARED_RESPONSE_BYTES = 64 << 20;
const ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const encoder = new TextEncoder();

type Backend = "kzg" | "ipa";
type Kind = "map" | "list";
type TargetKind = "unknown" | "cas" | Kind;
type Presence = "absent" | "present";

interface Coordinate {
  kind: Kind;
  map_path: string;
  list_index: number;
}

interface Target {
  kind: TargetKind;
  cid: string;
}

interface OptionalTarget extends Target {
  state: Presence;
}

interface OptionalCID {
  state: Presence;
  cid: string;
}

interface OptionalOutput {
  state: Presence;
  id: string;
  kind: "" | Kind;
}

interface CommitDescriptor {
  mode: "default" | "fixed_list";
  total_size: bigint;
  chunk_size: bigint;
}

interface ArcEntry {
  coordinate: Coordinate;
  target: Target;
}

interface UpdateObject {
  object_id: string;
  root: string;
  kind: Kind;
  entries: ArcEntry[];
  commit: CommitDescriptor;
}

interface UpdateView {
  profile: string;
  state_profile: string;
  base_root: string;
  bounds: {
    max_objects: number;
    max_total_entries: number;
    max_depth: number;
  };
  objects: UpdateObject[];
}

interface IntentChange {
  coordinate: Coordinate;
  before: OptionalTarget;
  after: OptionalTarget;
  output: OptionalOutput;
}

interface IntentTransition {
  id: string;
  object_id: string;
  old_root: OptionalCID;
  kind: Kind;
  backend: Backend;
  changes: IntentChange[];
  commit: CommitDescriptor;
  expected_uses: number;
}

interface SemanticIntent {
  profile: string;
  base_root: string;
  transitions: IntentTransition[];
  top_output_id: string;
}

interface ClientRootBundle {
  profile: string;
  operation_id: string;
  view: UpdateView;
  intent: SemanticIntent;
  outputs: Array<{ transition_id: string; root: string }>;
  candidate: string;
  payload_cids: string[];
  view_digest: string;
  intent_digest: string;
}

interface MaterializationReceipt {
  profile: string;
  operation_id: string;
  base_root: string;
  candidate: string;
  bundle_digest: string;
  durable_boundary: string;
}

interface WriterResult {
  profile: string;
  bundle: ClientRootBundle;
  next_view: UpdateView;
  metrics: Record<string, number>;
}

interface ParsedCID {
  raw: Uint8Array;
  key: string;
  codec: number;
  semantic: "unknown" | Kind;
  backend: "unknown" | Backend;
}

interface PreparedCandidate {
  result: WriterResult;
  bundleDigest: string;
  nextViewDigest: string;
  responseBytes: number;
}

interface CommitmentAPI {
  maltCommitmentLoadedBackend: Backend;
  maltCommitmentLoadObjectsV1(input: Uint8Array): Promise<string>;
  maltCommitmentApplyDeltaV1(input: Uint8Array): Promise<string>;
  maltCommitmentRetainRootsV1(input: Uint8Array): Promise<string>;
  maltCommitmentDropSessionV1(input: Uint8Array): Promise<string>;
}

export class MaltWriterTSSession {
  readonly backend: Backend;
  #api: CommitmentAPI;
  #view: UpdateView | undefined;
  #viewDigest = "";
  #prepared = new Map<string, PreparedCandidate>();
  #preparedResponseBytes = 0;
  #tail: Promise<void> = Promise.resolve();
  #wasmInputBytes = 0;
  #wasmOutputBytes = 0;
  #wasmCalls = 0;
  #closed = false;
  readonly #sessionID: string;

  constructor(backend: Backend, api: CommitmentAPI = globalThis as unknown as CommitmentAPI) {
    if (backend !== "kzg" && backend !== "ipa") {
      throw new Error(`unsupported commitment backend ${JSON.stringify(backend)}`);
    }
    if (
      api.maltCommitmentLoadedBackend !== backend ||
      typeof api.maltCommitmentLoadObjectsV1 !== "function" ||
      typeof api.maltCommitmentApplyDeltaV1 !== "function" ||
      typeof api.maltCommitmentRetainRootsV1 !== "function" ||
      typeof api.maltCommitmentDropSessionV1 !== "function"
    ) {
      throw new Error(`the ${backend} commitment WASM backend is not ready`);
    }
    this.backend = backend;
    this.#api = api;
    this.#sessionID = newSessionID();
  }

  load(updateView: unknown): Promise<string> {
    return this.#exclusive(async () => {
      this.#assertOpen();
      const view = normalizeUpdateView(updateView, this.backend);
      const request = {
        profile: OBJECTS_PROFILE,
        session_id: this.#sessionID,
        objects: view.objects.map((object) => ({
          object_id: object.object_id,
          root: object.root,
          kind: object.kind,
          entries: object.entries,
          commit: object.commit,
        })),
      };
      const requestBytes = encoder.encode(wireJSONStringify(request));
      const responseJSON = await this.#api.maltCommitmentLoadObjectsV1(requestBytes);
      this.#recordWASMTransfer(requestBytes, responseJSON);
      const response = parseCommitmentResult(responseJSON, this.backend, this.#sessionID);
      if (response.verified_objects !== view.objects.length) {
        throw new Error(
          `commitment backend verified ${response.verified_objects} objects, expected ${view.objects.length}`,
        );
      }
      this.#view = view;
      this.#viewDigest = await digestUpdateView(view);
      this.#prepared.clear();
      this.#preparedResponseBytes = 0;
      return view.base_root;
    });
  }

  prepare(operationID: string, semanticIntent: unknown): Promise<string> {
    return this.#exclusive(async () => {
      let candidateStored = false;
      try {
        this.#assertOpen();
        const totalStarted = performance.now();
        if (!this.#view) {
          throw new Error("TypeScript writer session has no update view");
        }
        if (!ID_PATTERN.test(operationID)) {
          throw new Error(`invalid operation id ${JSON.stringify(operationID)}`);
        }
        if (this.#prepared.has(operationID)) {
          throw new Error(`operation ${JSON.stringify(operationID)} is already prepared`);
        }
        if (this.#prepared.size >= MAX_PREPARED) {
          throw new Error(`TypeScript writer already retains ${MAX_PREPARED} prepared candidates`);
        }

      const intentStarted = performance.now();
      const intent = normalizeSemanticIntent(this.#view, semanticIntent, this.backend);
      const intentNormalizationNS = elapsedNS(intentStarted);
      const digestStarted = performance.now();
      const intentDigest = await digestSemanticIntent(intent);
      const digestNS = elapsedNS(digestStarted);

      const rootStarted = performance.now();
      const objects = new Map(this.#view.objects.map((object) => [object.object_id, cloneObject(object)]));
      const outputRoots = new Map<string, string>();
      const outputs: Array<{ transition_id: string; root: string }> = [];
      const payloads = new Map<string, string>();
      let commitmentNS = 0;
      for (const transition of intent.transitions) {
        const resolvedChanges = transition.changes.map((change) => {
          let after = change.after;
          if (change.output.state === "present") {
            const outputRoot = outputRoots.get(change.output.id);
            if (!outputRoot) {
              throw new Error(
                `transition ${JSON.stringify(transition.id)} consumes unavailable output ${JSON.stringify(change.output.id)}`,
              );
            }
            after = {
              state: "present",
              kind: change.output.kind as Kind,
              cid: outputRoot,
            };
          } else if (after.state === "present") {
            const parsed = parseCID(after.cid);
            if (parsed.semantic === "unknown") {
              payloads.set(parsed.key, after.cid);
            }
          }
          return {
            coordinate: change.coordinate,
            before: change.before,
            after,
          };
        });
        const delta = {
          profile: DELTA_PROFILE,
          session_id: this.#sessionID,
          base_root: this.#view.base_root,
          object_id: transition.object_id,
          old_root: transition.old_root,
          kind: transition.kind,
          changes: resolvedChanges,
          commit: transition.commit,
        };
        const deltaBytes = encoder.encode(wireJSONStringify(delta));
        const commitmentJSON = await this.#api.maltCommitmentApplyDeltaV1(deltaBytes);
        this.#recordWASMTransfer(deltaBytes, commitmentJSON);
        const commitment = parseCommitmentResult(commitmentJSON, this.backend, this.#sessionID);
        if (typeof commitment.root !== "string" || !Number.isSafeInteger(commitment.commitment_ns)) {
          throw new Error("commitment backend returned an invalid delta result");
        }
        const parsedRoot = parseCID(commitment.root);
        if (parsedRoot.backend !== transition.backend || parsedRoot.semantic !== transition.kind) {
          throw new Error(
            `commitment backend returned ${parsedRoot.semantic}/${parsedRoot.backend}, expected ${transition.kind}/${transition.backend}`,
          );
        }
        commitmentNS += commitment.commitment_ns;
        outputRoots.set(transition.id, commitment.root);
        outputs.push({ transition_id: transition.id, root: commitment.root });

        const current = objects.get(transition.object_id) ?? {
          object_id: transition.object_id,
          root: "",
          kind: transition.kind,
          entries: [],
          commit: transition.commit,
        };
        objects.set(transition.object_id, {
          object_id: transition.object_id,
          root: commitment.root,
          kind: transition.kind,
          entries: applyCompleteVector(current.entries, transition.kind, resolvedChanges),
          commit: cloneCommit(transition.commit),
        });
      }
      const candidate = outputRoots.get(intent.top_output_id);
      if (!candidate) {
        throw new Error("normalized intent did not produce its top output");
      }
      const rootComputationNS = elapsedNS(rootStarted);
      const expectedRootStarted = performance.now();
      if (candidate.length === 0) {
        throw new Error("candidate root encoding is empty");
      }
      const expectedRootEncodingNS = elapsedNS(expectedRootStarted);

      const bundleStarted = performance.now();
      outputs.sort((left, right) => compareStrings(left.transition_id, right.transition_id));
      const payloadCIDs = [...payloads.values()].sort(compareCIDStrings);
      const bundle: ClientRootBundle = {
        profile: BUNDLE_PROFILE,
        operation_id: operationID,
        view: this.#view,
        intent,
        outputs,
        candidate,
        payload_cids: payloadCIDs,
        view_digest: this.#viewDigest,
        intent_digest: intentDigest,
      };
      const bundleDigest = await digestBundle(bundle);
      const bundleValidationNS = elapsedNS(bundleStarted);

      const nextStarted = performance.now();
      const nextView = nextReachableView(this.#view, candidate, objects, this.backend);
      const nextViewDigest = await digestUpdateView(nextView);
      const nextViewNS = elapsedNS(nextStarted);
      const result: WriterResult = {
        profile: RESULT_PROFILE,
        bundle,
        next_view: nextView,
        metrics: {
          view_normalization_ns: 0,
          intent_normalization_ns: intentNormalizationNS,
          digest_ns: digestNS,
          commitment_update_ns: commitmentNS,
          root_computation_ns: rootComputationNS,
          expected_root_encoding_ns: expectedRootEncodingNS,
          bundle_validation_ns: bundleValidationNS,
          next_view_ns: nextViewNS,
          total_ns: elapsedNS(totalStarted),
        },
      };
      const resultJSON = wireJSONStringify(result);
      const responseBytes = encoder.encode(resultJSON).byteLength;
      if (responseBytes > MAX_PREPARED_RESPONSE_BYTES - this.#preparedResponseBytes) {
        throw new Error(
          `prepared TypeScript writer responses exceed ${MAX_PREPARED_RESPONSE_BYTES} retained bytes`,
        );
      }
      this.#prepared.set(operationID, { result, bundleDigest, nextViewDigest, responseBytes });
      this.#preparedResponseBytes += responseBytes;
      candidateStored = true;
      await this.#retainMaterializedState();
      return resultJSON;
      } catch (error) {
        if (candidateStored) {
          const candidate = this.#prepared.get(operationID);
          if (candidate) {
            this.#prepared.delete(operationID);
            this.#preparedResponseBytes -= candidate.responseBytes;
          }
        }
        if (this.#view && !this.#closed) {
          try {
            await this.#retainMaterializedState();
          } catch (cleanupError) {
            throw new AggregateError([error, cleanupError], "prepare failed and commitment cleanup also failed");
          }
        }
        throw error;
      }
    });
  }

  acceptReceipt(operationID: string, value: unknown): Promise<string> {
    return this.#exclusive(async () => {
      this.#assertOpen();
      const prepared = this.#prepared.get(operationID);
      if (!prepared) {
        throw new Error(`operation ${JSON.stringify(operationID)} is not prepared`);
      }
      const receipt = normalizeReceipt(value);
      const bundle = prepared.result.bundle;
      if (
        receipt.operation_id !== operationID ||
        receipt.base_root !== bundle.view.base_root ||
        receipt.candidate !== bundle.candidate ||
        receipt.bundle_digest !== prepared.bundleDigest
      ) {
        throw new Error("materialization receipt does not acknowledge the exact prepared bundle");
      }
      this.#view = prepared.result.next_view;
      this.#viewDigest = prepared.nextViewDigest;
      this.#prepared.clear();
      this.#preparedResponseBytes = 0;
      await this.#retainMaterializedState();
      return this.#view.base_root;
    });
  }

  discard(operationID: string): Promise<string> {
    return this.#exclusive(async () => {
      this.#assertOpen();
      const prepared = this.#prepared.get(operationID);
      if (!prepared) {
        throw new Error(`operation ${JSON.stringify(operationID)} is not prepared`);
      }
      this.#prepared.delete(operationID);
      this.#preparedResponseBytes -= prepared.responseBytes;
      await this.#retainMaterializedState();
      return operationID;
    });
  }

  close(): Promise<string> {
    return this.#exclusive(async () => {
      if (this.#closed) return this.#sessionID;
      const sessionBytes = encoder.encode(this.#sessionID);
      const dropped = await this.#api.maltCommitmentDropSessionV1(sessionBytes);
      this.#recordWASMTransfer(sessionBytes, dropped);
      if (dropped !== this.#sessionID) {
        throw new Error("commitment backend dropped an unexpected session");
      }
      this.#prepared.clear();
      this.#preparedResponseBytes = 0;
      this.#view = undefined;
      this.#closed = true;
      return dropped;
    });
  }

  diagnostics(): {
    wasm_calls: number;
    wasm_input_bytes: number;
    wasm_output_bytes: number;
  } {
    return {
      wasm_calls: this.#wasmCalls,
      wasm_input_bytes: this.#wasmInputBytes,
      wasm_output_bytes: this.#wasmOutputBytes,
    };
  }

  #recordWASMTransfer(input: Uint8Array, output: string): void {
    this.#wasmCalls++;
    this.#wasmInputBytes += input.byteLength;
    this.#wasmOutputBytes += encoder.encode(output).byteLength;
  }

  async #retainMaterializedState(): Promise<void> {
    if (!this.#view) return;
    const roots = new Map<string, { object_id: string; root: string }>();
    const addView = (view: UpdateView) => {
      for (const object of view.objects) {
        roots.set(`${object.object_id}\u0000${parseCID(object.root).key}`, {
          object_id: object.object_id,
          root: object.root,
        });
      }
    };
    addView(this.#view);
    for (const candidate of this.#prepared.values()) {
      addView(candidate.result.next_view);
    }
    const request = {
      profile: RETAIN_PROFILE,
      session_id: this.#sessionID,
      objects: [...roots.values()],
    };
    const input = encoder.encode(wireJSONStringify(request));
    const raw = await this.#api.maltCommitmentRetainRootsV1(input);
    this.#recordWASMTransfer(input, raw);
    parseCommitmentResult(raw, this.backend, this.#sessionID);
  }

  #assertOpen(): void {
    if (this.#closed) {
      throw new Error("TypeScript writer session is closed");
    }
  }

  #exclusive<T>(task: () => Promise<T>): Promise<T> {
    const result = this.#tail.then(task, task);
    this.#tail = result.then(() => undefined, () => undefined);
    return result;
  }
}

function normalizeUpdateView(value: unknown, backend: Backend): UpdateView {
  const input = exactRecord(
    value,
    "update view",
    ["profile", "state_profile", "base_root", "bounds", "objects"],
  );
  if (input.profile !== UPDATE_VIEW_PROFILE || input.state_profile !== STATE_PROFILE) {
    throw new Error("update view profile is invalid");
  }
  const baseRoot = requiredString(input.base_root, "update view base_root");
  const parsedBase = parseCID(baseRoot);
  if (parsedBase.backend !== backend) {
    throw new Error(`update view base backend is ${parsedBase.backend}, expected ${backend}`);
  }
  const boundsInput = exactRecord(
    input.bounds,
    "update view bounds",
    ["max_objects", "max_total_entries", "max_depth"],
  );
  const bounds = {
    max_objects: boundedInteger(boundsInput.max_objects, "max_objects", 1, MAX_OBJECTS),
    max_total_entries: boundedInteger(
      boundsInput.max_total_entries,
      "max_total_entries",
      1,
      MAX_ENTRIES,
    ),
    max_depth: boundedInteger(boundsInput.max_depth, "max_depth", 1, MAX_DEPTH),
  };
  if (!Array.isArray(input.objects) || input.objects.length === 0 ||
      input.objects.length > bounds.max_objects) {
    throw new Error("update view object count is outside bounds");
  }
  const objects = input.objects.map((object, index) =>
    normalizeUpdateObject(object, `update object ${index}`, backend)
  );
  const byID = new Set<string>();
  const byRoot = new Map<string, number>();
  let totalEntries = 0;
  for (let index = 0; index < objects.length; index++) {
    const object = objects[index];
    if (byID.has(object.object_id)) {
      throw new Error(`duplicate update object id ${JSON.stringify(object.object_id)}`);
    }
    const rootKey = parseCID(object.root).key;
    if (byRoot.has(rootKey)) {
      throw new Error(`duplicate update object root ${object.root}`);
    }
    byID.add(object.object_id);
    byRoot.set(rootKey, index);
    totalEntries += object.entries.length;
    if (totalEntries > bounds.max_total_entries) {
      throw new Error("update view total entries exceed bounds");
    }
  }
  objects.sort((left, right) => compareStrings(left.object_id, right.object_id));
  byRoot.clear();
  objects.forEach((object, index) => byRoot.set(parseCID(object.root).key, index));
  const baseIndex = byRoot.get(parsedBase.key);
  if (baseIndex === undefined) {
    throw new Error("update view base root object is missing");
  }

  const children: number[][] = objects.map(() => []);
  const indegree = objects.map(() => 0);
  objects.forEach((object, index) => {
    for (const entry of object.entries) {
      const target = parseCID(entry.target.cid);
      if (target.semantic === "unknown") {
        continue;
      }
      const childIndex = byRoot.get(target.key);
      if (childIndex === undefined) {
        throw new Error(
          `update object ${JSON.stringify(object.object_id)} references missing ${target.semantic} child ${entry.target.cid}`,
        );
      }
      if (objects[childIndex].kind !== target.semantic) {
        throw new Error(`child ${entry.target.cid} kind mismatch`);
      }
      children[index].push(childIndex);
      indegree[childIndex]++;
    }
  });
  const reachable = objects.map(() => false);
  const stack = [baseIndex];
  reachable[baseIndex] = true;
  let visited = 0;
  while (stack.length > 0) {
    const index = stack.pop()!;
    visited++;
    for (const child of children[index]) {
      if (!reachable[child]) {
        reachable[child] = true;
        stack.push(child);
      }
    }
  }
  if (visited !== objects.length) {
    throw new Error(`update view contains ${objects.length - visited} unreachable objects`);
  }
  const queue: number[] = [];
  indegree.forEach((count, index) => {
    if (count === 0) queue.push(index);
  });
  const depths = objects.map(() => 0);
  depths[baseIndex] = 1;
  let processed = 0;
  for (let head = 0; head < queue.length; head++) {
    const index = queue[head];
    processed++;
    for (const child of children[index]) {
      const depth = depths[index] + 1;
      if (depth > bounds.max_depth) {
        throw new Error("update view depth exceeds bounds");
      }
      depths[child] = Math.max(depths[child], depth);
      indegree[child]--;
      if (indegree[child] === 0) queue.push(child);
    }
  }
  if (processed !== objects.length) {
    throw new Error("update view contains a semantic cycle");
  }
  return {
    profile: UPDATE_VIEW_PROFILE,
    state_profile: STATE_PROFILE,
    base_root: baseRoot,
    bounds,
    objects,
  };
}

function normalizeUpdateObject(value: unknown, label: string, backend: Backend): UpdateObject {
  const input = exactRecord(
    value,
    label,
    ["object_id", "root", "kind", "entries", "commit"],
  );
  const objectID = requiredID(input.object_id, `${label} object_id`);
  const root = requiredString(input.root, `${label} root`);
  const kind = requiredKind(input.kind, `${label} kind`);
  const parsedRoot = parseCID(root);
  if (parsedRoot.semantic !== kind || parsedRoot.backend !== backend) {
    throw new Error(`${label} root kind/backend mismatch`);
  }
  if (!Array.isArray(input.entries) || input.entries.length > MAX_ENTRIES) {
    throw new Error(`${label} entries are missing or excessive`);
  }
  const entries = input.entries.map((entry, index) =>
    normalizeArcEntry(entry, kind, `${label} entry ${index}`)
  );
  entries.sort((left, right) =>
    compareBytes(coordinateBytes(left.coordinate), coordinateBytes(right.coordinate))
  );
  for (let index = 1; index < entries.length; index++) {
    if (compareBytes(
      coordinateBytes(entries[index - 1].coordinate),
      coordinateBytes(entries[index].coordinate),
    ) === 0) {
      throw new Error(`${label} has a duplicate coordinate`);
    }
  }
  if (kind === "list") {
    entries.forEach((entry, index) => {
      if (entry.coordinate.list_index !== index) {
        throw new Error(`${label} list vector is sparse at ${entry.coordinate.list_index}`);
      }
    });
  }
  const commit = normalizeCommit(input.commit, kind, entries.length, `${label} commit`);
  return { object_id: objectID, root, kind, entries, commit };
}

function normalizeSemanticIntent(
  view: UpdateView,
  value: unknown,
  backend: Backend,
): SemanticIntent {
  const input = exactRecord(
    value,
    "semantic intent",
    ["profile", "base_root", "transitions", "top_output_id"],
  );
  if (input.profile !== INTENT_PROFILE || input.base_root !== view.base_root) {
    throw new Error("semantic intent profile or base root is invalid");
  }
  const topOutputID = requiredID(input.top_output_id, "top_output_id");
  if (!Array.isArray(input.transitions) || input.transitions.length === 0 ||
      input.transitions.length > MAX_TRANSITIONS) {
    throw new Error("semantic intent transition count is invalid");
  }
  const objects = new Map(view.objects.map((object) => [object.object_id, object]));
  const transitions = new Map<string, IntentTransition>();
  const objectTransitions = new Set<string>();
  let totalChanges = 0;
  for (let index = 0; index < input.transitions.length; index++) {
    const transition = normalizeTransition(
      input.transitions[index], `transition ${index}`, objects, backend,
    );
    totalChanges += transition.changes.length;
    if (totalChanges > MAX_CHANGES) {
      throw new Error("semantic intent changes exceed the wire limit");
    }
    if (transitions.has(transition.id) || objectTransitions.has(transition.object_id)) {
      throw new Error("semantic intent has duplicate transition or object ids");
    }
    transitions.set(transition.id, transition);
    objectTransitions.add(transition.object_id);
  }
  const top = transitions.get(topOutputID);
  if (!top || top.old_root.state !== "present" ||
      top.old_root.cid !== view.base_root || top.expected_uses !== 0) {
    throw new Error("top output must update the base root and have zero uses");
  }

  const uses = new Map<string, number>();
  const parents = new Map<string, string[]>();
  const indegree = new Map<string, number>(
    [...transitions.keys()].map((id) => [id, 0]),
  );
  for (const [parentID, transition] of transitions) {
    for (const change of transition.changes) {
      if (change.output.state !== "present") continue;
      const child = transitions.get(change.output.id);
      if (!child) {
        throw new Error(
          `transition ${JSON.stringify(parentID)} references unknown output ${JSON.stringify(change.output.id)}`,
        );
      }
      if (child.kind !== change.output.kind) {
        throw new Error(`output ${JSON.stringify(change.output.id)} kind mismatch`);
      }
      uses.set(change.output.id, (uses.get(change.output.id) ?? 0) + 1);
      parents.set(change.output.id, [...(parents.get(change.output.id) ?? []), parentID]);
      indegree.set(parentID, (indegree.get(parentID) ?? 0) + 1);
    }
  }
  for (const [id, transition] of transitions) {
    const observed = uses.get(id) ?? 0;
    if (observed !== transition.expected_uses) {
      throw new Error(
        `output ${JSON.stringify(id)} declares ${transition.expected_uses} uses, observed ${observed}`,
      );
    }
    if (id !== topOutputID && observed === 0) {
      throw new Error(`output ${JSON.stringify(id)} is orphaned`);
    }
  }
  const ready = [...indegree]
    .filter(([, degree]) => degree === 0)
    .map(([id]) => id)
    .sort();
  const ordered: IntentTransition[] = [];
  while (ready.length > 0) {
    const id = ready.shift()!;
    ordered.push(transitions.get(id)!);
    for (const parent of parents.get(id) ?? []) {
      const degree = (indegree.get(parent) ?? 0) - 1;
      indegree.set(parent, degree);
      if (degree === 0) {
        ready.push(parent);
        ready.sort();
      }
    }
  }
  if (ordered.length !== transitions.size || ordered.at(-1)?.id !== topOutputID) {
    throw new Error("semantic intent dependency graph is cyclic or does not close at top");
  }
  return {
    profile: INTENT_PROFILE,
    base_root: view.base_root,
    transitions: ordered,
    top_output_id: topOutputID,
  };
}

function normalizeTransition(
  value: unknown,
  label: string,
  objects: Map<string, UpdateObject>,
  backend: Backend,
): IntentTransition {
  const input = exactRecord(
    value,
    label,
    [
      "id", "object_id", "old_root", "kind", "backend", "changes",
      "commit", "expected_uses",
    ],
  );
  const id = requiredID(input.id, `${label} id`);
  const objectID = requiredID(input.object_id, `${label} object_id`);
  const oldRoot = normalizeOptionalCID(input.old_root, `${label} old_root`);
  const kind = requiredKind(input.kind, `${label} kind`);
  if (input.backend !== backend) {
    throw new Error(`${label} backend ${JSON.stringify(input.backend)} is unavailable`);
  }
  const object = objects.get(objectID);
  if (oldRoot.state === "present") {
    if (!object || object.root !== oldRoot.cid || object.kind !== kind) {
      throw new Error(`${label} old object does not match update view`);
    }
    const parsed = parseCID(oldRoot.cid);
    if (parsed.backend !== backend) {
      throw new Error(`${label} backend does not match old root`);
    }
  } else if (object) {
    throw new Error(`${label} declares an existing object as new`);
  }
  if (!Array.isArray(input.changes) || input.changes.length === 0 ||
      input.changes.length > MAX_CHANGES) {
    throw new Error(`${label} changes are empty or excessive`);
  }
  const currentEntries = new Map<string, Target>(
    (object?.entries ?? []).map((entry) => [
      bytesKey(coordinateBytes(entry.coordinate)),
      entry.target,
    ]),
  );
  const changes = input.changes.map((change, index) =>
    normalizeIntentChange(change, kind, currentEntries, `${label} change ${index}`)
  );
  changes.sort((left, right) =>
    compareBytes(coordinateBytes(left.coordinate), coordinateBytes(right.coordinate))
  );
  for (let index = 1; index < changes.length; index++) {
    if (compareBytes(
      coordinateBytes(changes[index - 1].coordinate),
      coordinateBytes(changes[index].coordinate),
    ) === 0) {
      throw new Error(`${label} has duplicate change coordinates`);
    }
  }
  const expectedUses = boundedInteger(
    input.expected_uses, `${label} expected_uses`, 0, 0xffffffff,
  );
  const postCount = kind === "list"
    ? postTransitionListCount(object?.entries ?? [], changes, label)
    : 0;
  const commit = normalizeCommit(input.commit, kind, postCount, `${label} commit`);
  if (kind === "list" && object) {
    validateListCommitTransition(object, commit, postCount, label);
  }
  return {
    id,
    object_id: objectID,
    old_root: oldRoot,
    kind,
    backend,
    changes,
    commit,
    expected_uses: expectedUses,
  };
}

function normalizeIntentChange(
  value: unknown,
  kind: Kind,
  entries: Map<string, Target>,
  label: string,
): IntentChange {
  const input = exactRecord(value, label, ["coordinate", "before", "after", "output"]);
  const coordinate = normalizeCoordinate(input.coordinate, kind, `${label} coordinate`);
  const before = normalizeOptionalTarget(input.before, `${label} before`);
  const after = normalizeOptionalTarget(input.after, `${label} after`);
  const output = normalizeOptionalOutput(input.output, `${label} output`);
  if (after.state === "present" && output.state === "present") {
    throw new Error(`${label} has both literal and output post-images`);
  }
  const current = entries.get(bytesKey(coordinateBytes(coordinate)));
  if (before.state === "absent") {
    if (current) throw new Error(`${label} expected the coordinate absent`);
  } else if (!current || !targetsEqual(before, current)) {
    throw new Error(`${label} before-image mismatch`);
  }
  if (before.state === "absent" && after.state === "absent" && output.state === "absent") {
    throw new Error(`${label} is empty`);
  }
  if (before.state === "present" && after.state === "present" &&
      targetsEqual(before, after)) {
    throw new Error(`${label} is a no-op`);
  }
  return { coordinate, before, after, output };
}

function nextReachableView(
  previous: UpdateView,
  candidate: string,
  objects: Map<string, UpdateObject>,
  backend: Backend,
): UpdateView {
  const ordered = [...objects.values()].sort((left, right) =>
    compareStrings(left.object_id, right.object_id)
  );
  const byRoot = new Map<string, UpdateObject>();
  for (const object of ordered) {
    const key = parseCID(object.root).key;
    if (byRoot.has(key)) {
      throw new Error(`retained objects converge to root ${object.root}`);
    }
    byRoot.set(key, object);
  }
  const rootObject = byRoot.get(parseCID(candidate).key);
  if (!rootObject) {
    throw new Error("candidate root has no retained complete vector");
  }
  const reachable = new Map<string, UpdateObject>();
  const pending = [rootObject];
  while (pending.length > 0) {
    const object = pending.pop()!;
    if (reachable.has(object.object_id)) continue;
    reachable.set(object.object_id, object);
    for (const entry of object.entries) {
      const target = parseCID(entry.target.cid);
      if (target.semantic === "unknown") continue;
      const child = byRoot.get(target.key);
      if (!child) {
        throw new Error(
          `retained object ${JSON.stringify(object.object_id)} references missing child ${entry.target.cid}`,
        );
      }
      if (!reachable.has(child.object_id)) pending.push(child);
    }
  }
  return normalizeUpdateView({
    profile: previous.profile,
    state_profile: previous.state_profile,
    base_root: candidate,
    bounds: previous.bounds,
    objects: [...reachable.values()],
  }, backend);
}

function applyCompleteVector(
  current: ArcEntry[],
  kind: Kind,
  changes: Array<{
    coordinate: Coordinate;
    before: OptionalTarget;
    after: OptionalTarget;
  }>,
): ArcEntry[] {
  const entries = new Map<string, ArcEntry>(
    current.map((entry) => [bytesKey(coordinateBytes(entry.coordinate)), entry]),
  );
  for (const change of changes) {
    const key = bytesKey(coordinateBytes(change.coordinate));
    if (change.after.state === "absent") {
      entries.delete(key);
    } else {
      entries.set(key, {
        coordinate: cloneCoordinate(change.coordinate),
        target: { kind: change.after.kind, cid: change.after.cid },
      });
    }
  }
  const result = [...entries.values()].sort((left, right) =>
    compareBytes(coordinateBytes(left.coordinate), coordinateBytes(right.coordinate))
  );
  if (kind === "list") {
    result.forEach((entry, index) => {
      if (entry.coordinate.list_index !== index) {
        throw new Error("list transition creates a sparse post-image");
      }
    });
  }
  return result;
}

async function digestUpdateView(view: UpdateView): Promise<string> {
  const output = new ByteWriter();
  output.string(view.profile);
  output.string(view.state_profile);
  output.cid(view.base_root);
  output.u32(view.bounds.max_objects);
  output.u64(view.bounds.max_total_entries);
  output.u32(view.bounds.max_depth);
  output.u32(view.objects.length);
  for (const object of view.objects) {
    output.string(object.object_id);
    output.cid(object.root);
    output.string(object.kind);
    output.bytes(marshalArcSet(object));
    output.commit(object.commit);
  }
  return sha256Hex(output.finish());
}

async function digestSemanticIntent(intent: SemanticIntent): Promise<string> {
  const output = new ByteWriter();
  output.string(intent.profile);
  output.cid(intent.base_root);
  output.string(intent.top_output_id);
  output.u32(intent.transitions.length);
  for (const transition of intent.transitions) {
    output.string(transition.id);
    output.string(transition.object_id);
    output.optionalCID(transition.old_root);
    output.string(transition.kind);
    output.string(transition.backend);
    output.commit(transition.commit);
    output.u32(transition.expected_uses);
    output.u32(transition.changes.length);
    for (const change of transition.changes) {
      output.bytes(coordinateBytes(change.coordinate));
      output.optionalTarget(change.before);
      output.optionalTarget(change.after);
      output.string(change.output.state === "present" ? change.output.id : "");
      output.string(change.output.state === "present" ? change.output.kind : "");
    }
  }
  return sha256Hex(output.finish());
}

async function digestBundle(bundle: ClientRootBundle): Promise<string> {
  const output = new ByteWriter();
  output.string(bundle.profile);
  output.string(bundle.operation_id);
  output.bytes(hexBytes(bundle.view_digest));
  output.bytes(hexBytes(bundle.intent_digest));
  output.cid(bundle.candidate);
  output.u32(bundle.outputs.length);
  for (const item of bundle.outputs) {
    output.string(item.transition_id);
    output.cid(item.root);
  }
  output.u32(bundle.payload_cids.length);
  for (const payload of bundle.payload_cids) output.cid(payload);
  return sha256Hex(output.finish());
}

function marshalArcSet(object: UpdateObject): Uint8Array {
  const output = new ByteWriter();
  output.raw(encoder.encode("MARC"));
  output.byte(1);
  output.shortBytes(encoder.encode(object.kind));
  output.u32(object.entries.length);
  for (const entry of object.entries) {
    output.shortBytes(coordinateBytes(entry.coordinate));
    output.shortBytes(encoder.encode(entry.target.kind));
    output.shortBytes(parseCID(entry.target.cid).raw);
  }
  return output.finish();
}

class ByteWriter {
  #parts: Uint8Array[] = [];
  #length = 0;

  raw(value: Uint8Array): void {
    this.#parts.push(value);
    this.#length += value.length;
  }

  byte(value: number): void {
    this.raw(Uint8Array.of(value));
  }

  u32(value: number): void {
    const raw = new Uint8Array(4);
    new DataView(raw.buffer).setUint32(0, value, false);
    this.raw(raw);
  }

  u64(value: number | bigint): void {
    const integer = typeof value === "bigint" ? value : BigInt(value);
    if (
      integer < 0n || integer > 0xffff_ffff_ffff_ffffn ||
      (typeof value === "number" && (!Number.isSafeInteger(value) || value < 0))
    ) {
      throw new Error(`uint64 value ${value} is outside the uint64 domain`);
    }
    const raw = new Uint8Array(8);
    new DataView(raw.buffer).setBigUint64(0, integer, false);
    this.raw(raw);
  }

  bytes(value: Uint8Array): void {
    this.u64(value.length);
    this.raw(value);
  }

  shortBytes(value: Uint8Array): void {
    this.u32(value.length);
    this.raw(value);
  }

  string(value: string): void {
    this.bytes(encoder.encode(value));
  }

  cid(value: string): void {
    this.bytes(value === "" ? new Uint8Array() : parseCID(value).raw);
  }

  optionalCID(value: OptionalCID): void {
    this.cid(value.state === "present" ? value.cid : "");
  }

  optionalTarget(value: OptionalTarget): void {
    if (value.state === "absent") {
      this.byte(0);
      return;
    }
    this.byte(1);
    this.string(value.kind);
    this.cid(value.cid);
  }

  commit(value: CommitDescriptor): void {
    if (value.mode === "default") {
      this.byte(0);
      return;
    }
    this.byte(1);
    this.u64(value.total_size);
    this.u64(value.chunk_size);
  }

  finish(): Uint8Array {
    const result = new Uint8Array(this.#length);
    let offset = 0;
    for (const part of this.#parts) {
      result.set(part, offset);
      offset += part.length;
    }
    return result;
  }
}

const MAX_CID_CACHE_ENTRIES = 4096;
const cidCache = new Map<string, ParsedCID>();

function parseCID(value: string): ParsedCID {
  const cached = cidCache.get(value);
  if (cached) return cached;
  let raw: Uint8Array;
  let codec: number;
  if (/^b[a-z2-7]+$/.test(value)) {
    raw = decodeBase32(value.slice(1));
    if (`b${encodeBase32(raw)}` !== value) {
      throw new Error(`CID ${JSON.stringify(value)} is not canonical base32`);
    }
    const version = readVarint(raw, 0);
    if (version.value !== 1) throw new Error("CIDv1 version is invalid");
    const parsedCodec = readVarint(raw, version.next);
    codec = parsedCodec.value;
    validateMultihash(raw, parsedCodec.next);
  } else if (/^Qm[1-9A-HJ-NP-Za-km-z]+$/.test(value)) {
    raw = decodeBase58(value);
    if (encodeBase58(raw) !== value) throw new Error("CIDv0 is not canonical base58");
    codec = 0x70;
    const digest = validateMultihash(raw, 0);
    if (digest.code !== 0x12 || digest.length !== 32) {
      throw new Error("CIDv0 must carry a SHA-256 multihash");
    }
  } else {
    throw new Error(`CID ${JSON.stringify(value)} is not a canonical CID string`);
  }
  let semantic: ParsedCID["semantic"] = "unknown";
  let backend: ParsedCID["backend"] = "unknown";
  if (codec >= 0x300000 && codec <= 0x30ffff) {
    const offset = codec - 0x300000;
    const version = offset >>> 12;
    const semanticID = (offset >>> 8) & 0x0f;
    const backendID = offset & 0xff;
    if (version === 2 && (semanticID === 1 || semanticID === 2) &&
        (backendID === 1 || backendID === 2)) {
      semantic = semanticID === 1 ? "map" : "list";
      backend = backendID === 1 ? "kzg" : "ipa";
      const versionVarint = readVarint(raw, 0);
      const codecVarint = readVarint(raw, versionVarint.next);
      const multihash = validateMultihash(raw, codecVarint.next);
      const expectedLength = backend === "kzg" ? 48 : 32;
      if (multihash.code !== 0 || multihash.length !== expectedLength) {
        semantic = "unknown";
        backend = "unknown";
      }
    }
  }
  const parsed = { raw, key: bytesKey(raw), codec, semantic, backend };
  if (cidCache.size >= MAX_CID_CACHE_ENTRIES) {
    cidCache.delete(cidCache.keys().next().value!);
  }
  cidCache.set(value, parsed);
  return parsed;
}

function validateMultihash(
  raw: Uint8Array,
  offset: number,
): { code: number; length: number } {
  const code = readVarint(raw, offset);
  const length = readVarint(raw, code.next);
  if (length.value < 0 || length.next + length.value !== raw.length) {
    throw new Error("CID multihash length is invalid");
  }
  return { code: code.value, length: length.value };
}

function readVarint(raw: Uint8Array, offset: number): { value: number; next: number } {
  let value = 0;
  let shift = 0;
  for (let index = offset; index < raw.length && index < offset + 10; index++) {
    const byte = raw[index];
    value += (byte & 0x7f) * (2 ** shift);
    if (!Number.isSafeInteger(value)) throw new Error("CID varint exceeds safe integer range");
    if ((byte & 0x80) === 0) {
      if (index > offset && byte === 0) throw new Error("CID varint is not minimally encoded");
      return { value, next: index + 1 };
    }
    shift += 7;
  }
  throw new Error("CID contains an invalid varint");
}

function decodeBase32(value: string): Uint8Array {
  const alphabet = "abcdefghijklmnopqrstuvwxyz234567";
  const output: number[] = [];
  let buffer = 0;
  let bits = 0;
  for (const character of value) {
    const digit = alphabet.indexOf(character);
    if (digit < 0) throw new Error("invalid base32 CID");
    buffer = (buffer * 32) + digit;
    bits += 5;
    while (bits >= 8) {
      bits -= 8;
      output.push(Math.floor(buffer / (2 ** bits)) & 0xff);
      buffer %= 2 ** bits;
    }
  }
  if (bits > 0 && buffer !== 0) throw new Error("base32 CID has non-zero padding bits");
  return Uint8Array.from(output);
}

function encodeBase32(raw: Uint8Array): string {
  const alphabet = "abcdefghijklmnopqrstuvwxyz234567";
  let result = "";
  let buffer = 0;
  let bits = 0;
  for (const byte of raw) {
    buffer = (buffer * 256) + byte;
    bits += 8;
    while (bits >= 5) {
      bits -= 5;
      result += alphabet[Math.floor(buffer / (2 ** bits)) & 31];
      buffer %= 2 ** bits;
    }
  }
  if (bits > 0) result += alphabet[(buffer << (5 - bits)) & 31];
  return result;
}

function decodeBase58(value: string): Uint8Array {
  const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";
  let number = 0n;
  for (const character of value) {
    const digit = alphabet.indexOf(character);
    if (digit < 0) throw new Error("invalid base58 CID");
    number = number * 58n + BigInt(digit);
  }
  const bytes: number[] = [];
  while (number > 0) {
    bytes.push(Number(number & 0xffn));
    number >>= 8n;
  }
  bytes.reverse();
  let zeroes = 0;
  while (zeroes < value.length && value[zeroes] === "1") zeroes++;
  return Uint8Array.from([...new Array(zeroes).fill(0), ...bytes]);
}

function encodeBase58(raw: Uint8Array): string {
  const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";
  let number = 0n;
  for (const byte of raw) number = (number << 8n) | BigInt(byte);
  let result = "";
  while (number > 0) {
    result = alphabet[Number(number % 58n)] + result;
    number /= 58n;
  }
  let zeroes = 0;
  while (zeroes < raw.length && raw[zeroes] === 0) zeroes++;
  return "1".repeat(zeroes) + result;
}

function normalizeArcEntry(value: unknown, kind: Kind, label: string): ArcEntry {
  const input = exactRecord(value, label, ["coordinate", "target"]);
  return {
    coordinate: normalizeCoordinate(input.coordinate, kind, `${label} coordinate`),
    target: normalizeTarget(input.target, `${label} target`),
  };
}

function normalizeCoordinate(value: unknown, kind: Kind, label: string): Coordinate {
  const input = exactRecord(value, label, ["kind", "map_path", "list_index"]);
  if (input.kind !== kind) throw new Error(`${label} kind mismatch`);
  if (kind === "map") {
    const path = requiredString(input.map_path, `${label} map_path`);
    if (path.split("/").filter(Boolean).join("/") !== path || input.list_index !== 0) {
      throw new Error(`${label} map coordinate is not canonical`);
    }
    return { kind, map_path: path, list_index: 0 };
  }
  if (input.map_path !== "") throw new Error(`${label} list coordinate has map_path`);
  return {
    kind,
    map_path: "",
    list_index: boundedInteger(input.list_index, `${label} list_index`, 0, Number.MAX_SAFE_INTEGER),
  };
}

function normalizeTarget(value: unknown, label: string): Target {
  const input = exactRecord(value, label, ["kind", "cid"]);
  const kind = requiredTargetKind(input.kind, `${label} kind`);
  const cid = requiredString(input.cid, `${label} cid`);
  validateTargetKindCID(kind, parseCID(cid), label);
  return { kind, cid };
}

function normalizeOptionalTarget(value: unknown, label: string): OptionalTarget {
  const input = exactRecord(value, label, ["state", "kind", "cid"]);
  if (input.state === "absent") {
    if (input.kind !== "" || input.cid !== "") {
      throw new Error(`${label} absent target has companion fields`);
    }
    return { state: "absent", kind: "" as TargetKind, cid: "" };
  }
  if (input.state !== "present") throw new Error(`${label} presence is invalid`);
  const target = normalizeTarget({ kind: input.kind, cid: input.cid }, label);
  return { state: "present", ...target };
}

function normalizeOptionalCID(value: unknown, label: string): OptionalCID {
  const input = exactRecord(value, label, ["state", "cid"]);
  if (input.state === "absent") {
    if (input.cid !== "") throw new Error(`${label} absent CID has a companion value`);
    return { state: "absent", cid: "" };
  }
  if (input.state !== "present") throw new Error(`${label} presence is invalid`);
  const cid = requiredString(input.cid, `${label} cid`);
  parseCID(cid);
  return { state: "present", cid };
}

function normalizeOptionalOutput(value: unknown, label: string): OptionalOutput {
  const input = exactRecord(value, label, ["state", "id", "kind"]);
  if (input.state === "absent") {
    if (input.id !== "" || input.kind !== "") {
      throw new Error(`${label} absent output has companion fields`);
    }
    return { state: "absent", id: "", kind: "" };
  }
  if (input.state !== "present") throw new Error(`${label} presence is invalid`);
  return {
    state: "present",
    id: requiredID(input.id, `${label} id`),
    kind: requiredKind(input.kind, `${label} kind`),
  };
}

function normalizeCommit(
  value: unknown,
  kind: Kind,
  entryCount: number,
  label: string,
): CommitDescriptor {
  const input = exactRecord(value, label, ["mode", "total_size", "chunk_size"]);
  const totalSize = uint64Input(input.total_size, `${label} total_size`);
  const chunkSize = uint64Input(input.chunk_size, `${label} chunk_size`);
  if (input.mode === "default") {
    if (totalSize !== 0n || chunkSize !== 0n) {
      throw new Error(`${label} default mode has fixed-list fields`);
    }
    return { mode: "default", total_size: 0n, chunk_size: 0n };
  }
  if (input.mode !== "fixed_list" || kind !== "list" || chunkSize === 0n) {
    throw new Error(`${label} fixed-list descriptor is invalid`);
  }
  const implied = (totalSize + chunkSize - 1n) / chunkSize;
  if (implied !== BigInt(entryCount)) {
    throw new Error(`${label} implies ${implied} chunks, vector has ${entryCount}`);
  }
  return { mode: "fixed_list", total_size: totalSize, chunk_size: chunkSize };
}

function uint64Input(value: unknown, label: string): bigint {
  let integer: bigint;
  if (typeof value === "bigint") {
    integer = value;
  } else if (typeof value === "number") {
    if (!Number.isSafeInteger(value) || value < 0) {
      throw new Error(`${label} number is not an exact non-negative integer; use bigint or a decimal string`);
    }
    integer = BigInt(value);
  } else if (typeof value === "string" && /^(0|[1-9][0-9]*)$/.test(value)) {
    integer = BigInt(value);
  } else {
    throw new Error(`${label} is not a uint64`);
  }
  if (integer < 0n || integer > 0xffff_ffff_ffff_ffffn) {
    throw new Error(`${label} is outside the uint64 domain`);
  }
  return integer;
}

function normalizeReceipt(value: unknown): MaterializationReceipt {
  const input = exactRecord(
    value,
    "materialization receipt",
    ["profile", "operation_id", "base_root", "candidate", "bundle_digest", "durable_boundary"],
  );
  if (input.profile !== RECEIPT_PROFILE) throw new Error("receipt profile is invalid");
  const operationID = requiredID(input.operation_id, "receipt operation_id");
  const baseRoot = requiredString(input.base_root, "receipt base_root");
  const candidate = requiredString(input.candidate, "receipt candidate");
  parseCID(baseRoot);
  parseCID(candidate);
  const bundleDigest = requiredString(input.bundle_digest, "receipt bundle_digest");
  if (!/^[0-9a-f]{64}$/.test(bundleDigest)) {
    throw new Error("receipt bundle_digest is not canonical SHA-256 hex");
  }
  const boundary = requiredString(input.durable_boundary, "receipt durable_boundary");
  if (boundary.trim() === "") throw new Error("receipt durable boundary is empty");
  return {
    profile: RECEIPT_PROFILE,
    operation_id: operationID,
    base_root: baseRoot,
    candidate,
    bundle_digest: bundleDigest,
    durable_boundary: boundary,
  };
}

function parseCommitmentResult(
  raw: string,
  backend: Backend,
  sessionID: string,
): Record<string, any> {
  const value = JSON.parse(raw);
  const input = value as Record<string, any>;
  if (
    !input || typeof input !== "object" ||
    input.profile !== COMMITMENT_RESULT_PROFILE ||
    input.backend !== backend ||
    input.session_id !== sessionID
  ) {
    throw new Error("commitment backend returned an invalid result profile");
  }
  return input;
}

function validateTargetKindCID(kind: TargetKind, cid: ParsedCID, label: string): void {
  if (kind === "map" || kind === "list") {
    if (cid.semantic !== kind) throw new Error(`${label} semantic target kind/CID mismatch`);
  } else if (cid.semantic !== "unknown") {
    throw new Error(`${label} opaque target relabels a MALT semantic root`);
  }
}

function validateListCommitTransition(
  object: UpdateObject,
  commit: CommitDescriptor,
  postCount: number,
  label: string,
): void {
  if ((object.commit.mode === "fixed_list") !== (commit.mode === "fixed_list")) {
    throw new Error(`${label} changes plain/fixed list representation`);
  }
  if (commit.mode === "default") return;
  if (object.commit.chunk_size !== commit.chunk_size) {
    throw new Error(`${label} changes fixed-list chunk size`);
  }
  const oldCount = object.entries.length;
  if (postCount < oldCount) throw new Error(`${label} truncates a fixed list`);
  if (postCount === oldCount && object.commit.total_size !== commit.total_size) {
    throw new Error(`${label} replacement changes fixed-list total size`);
  }
  if (postCount > oldCount && object.commit.total_size % object.commit.chunk_size !== 0n) {
    throw new Error(`${label} appends to an unaligned fixed list`);
  }
  if (postCount > oldCount && commit.total_size <= object.commit.total_size) {
    throw new Error(`${label} fixed-list append does not increase total size`);
  }
}

function postTransitionListCount(
  entries: ArcEntry[],
  changes: IntentChange[],
  label: string,
): number {
  const present = new Set(entries.map((entry) => entry.coordinate.list_index));
  for (const change of changes) {
    const index = change.coordinate.list_index;
    if (change.after.state === "absent" && change.output.state === "absent") {
      present.delete(index);
    } else {
      present.add(index);
    }
  }
  for (let index = 0; index < present.size; index++) {
    if (!present.has(index)) throw new Error(`${label} creates a sparse list post-image`);
  }
  return present.size;
}

function coordinateBytes(value: Coordinate): Uint8Array {
  if (value.kind === "map") return encoder.encode(value.map_path);
  const result = new Uint8Array(8);
  new DataView(result.buffer).setBigUint64(0, BigInt(value.list_index), false);
  return result;
}

function cloneObject(value: UpdateObject): UpdateObject {
  return {
    object_id: value.object_id,
    root: value.root,
    kind: value.kind,
    entries: value.entries.map((entry) => ({
      coordinate: cloneCoordinate(entry.coordinate),
      target: { ...entry.target },
    })),
    commit: cloneCommit(value.commit),
  };
}

function cloneCoordinate(value: Coordinate): Coordinate {
  return { ...value };
}

function cloneCommit(value: CommitDescriptor): CommitDescriptor {
  return { ...value };
}

function targetsEqual(left: Target, right: Target): boolean {
  return left.kind === right.kind && left.cid === right.cid;
}

function compareCIDStrings(left: string, right: string): number {
  return compareBytes(parseCID(left).raw, parseCID(right).raw);
}

function compareBytes(left: Uint8Array, right: Uint8Array): number {
  const length = Math.min(left.length, right.length);
  for (let index = 0; index < length; index++) {
    if (left[index] !== right[index]) return left[index] - right[index];
  }
  return left.length - right.length;
}

function compareStrings(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

function newSessionID(): string {
  const random = new Uint8Array(16);
  crypto.getRandomValues(random);
  return `ts-${bytesKey(random)}`;
}

function wireJSONStringify(value: unknown): string {
  if (value === null) return "null";
  switch (typeof value) {
    case "string":
      return JSON.stringify(value);
    case "boolean":
      return value ? "true" : "false";
    case "number":
      if (!Number.isFinite(value)) throw new Error("wire JSON contains a non-finite number");
      return JSON.stringify(value);
    case "bigint":
      if (value < 0n || value > 0xffff_ffff_ffff_ffffn) {
        throw new Error("wire JSON bigint is outside the uint64 domain");
      }
      return value.toString(10);
    case "object":
      if (Array.isArray(value)) {
        return `[${value.map((item) => wireJSONStringify(item)).join(",")}]`;
      }
      return `{${Object.entries(value as Record<string, unknown>).map(([key, item]) =>
        `${JSON.stringify(key)}:${wireJSONStringify(item)}`
      ).join(",")}}`;
    default:
      throw new Error(`wire JSON cannot encode ${typeof value}`);
  }
}

function bytesKey(value: Uint8Array): string {
  return [...value].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function sha256Hex(value: Uint8Array): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", value));
  return bytesKey(digest);
}

function hexBytes(value: string): Uint8Array {
  if (!/^[0-9a-f]{64}$/.test(value)) throw new Error("digest is not canonical SHA-256 hex");
  return Uint8Array.from(value.match(/../g)!.map((pair) => Number.parseInt(pair, 16)));
}

function exactRecord(
  value: unknown,
  label: string,
  expectedKeys: string[],
): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  const input = value as Record<string, unknown>;
  const actual = Object.keys(input).sort();
  const expected = expectedKeys.toSorted();
  if (actual.length !== expected.length ||
      actual.some((key, index) => key !== expected[index])) {
    throw new Error(`${label} fields are invalid`);
  }
  for (const key of actual) {
    if (input[key] === null || input[key] === undefined) {
      throw new Error(`${label}.${key} must not be null`);
    }
  }
  return input;
}

function requiredString(value: unknown, label: string): string {
  if (typeof value !== "string" || value.length === 0) {
    throw new Error(`${label} must be a non-empty string`);
  }
  return value;
}

function requiredID(value: unknown, label: string): string {
  const result = requiredString(value, label);
  if (!ID_PATTERN.test(result)) throw new Error(`${label} is invalid`);
  return result;
}

function requiredKind(value: unknown, label: string): Kind {
  if (value !== "map" && value !== "list") throw new Error(`${label} is invalid`);
  return value;
}

function requiredTargetKind(value: unknown, label: string): TargetKind {
  if (value !== "unknown" && value !== "cas" && value !== "map" && value !== "list") {
    throw new Error(`${label} is invalid`);
  }
  return value;
}

function boundedInteger(
  value: unknown,
  label: string,
  minimum: number,
  maximum: number,
): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum || (value as number) > maximum) {
    throw new Error(`${label} must be a safe integer in ${minimum}..${maximum}`);
  }
  return value as number;
}

function elapsedNS(started: number): number {
  return Math.max(0, Math.round((performance.now() - started) * 1e6));
}
