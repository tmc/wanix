export const WanixCheckpointProtocol = Object.freeze({
	version: 1,
	type: "wanix-checkpoint",
	saveStateOp: "save-state",
});

export function isWanixCheckpointSaveState(message) {
	return !!message &&
		message.type === WanixCheckpointProtocol.type &&
		message.op === WanixCheckpointProtocol.saveStateOp &&
		(message.version === undefined || message.version === WanixCheckpointProtocol.version);
}

export function wanixCheckpointResponse(message, fields) {
	return {
		type: WanixCheckpointProtocol.type,
		op: WanixCheckpointProtocol.saveStateOp,
		version: WanixCheckpointProtocol.version,
		id: message && message.id || "",
		...fields,
	};
}

export async function wanixCheckpointSaveStateResponse(message, save) {
	if (typeof save !== "function") {
		return wanixCheckpointResponse(message, {
			ok: false,
			error: "migration unsupported",
		});
	}
	try {
		return wanixCheckpointResponse(message, {
			ok: true,
			state: await save(message),
		});
	} catch (error) {
		return wanixCheckpointResponse(message, {
			ok: false,
			error: String(error && error.message || error),
		});
	}
}

export async function postWanixCheckpointSaveState(message, options = {}) {
	const target = options.target || globalThis;
	const save = options.save || globalThis.wanixCheckpointState;
	target.postMessage(await wanixCheckpointSaveStateResponse(message, save));
}

export function wanixCheckpointStateFromWorker(worker) {
	return worker && worker.checkpoint_state || null;
}

export function installWanixCheckpoint(options = {}) {
	const target = options.target || globalThis;
	const load = options.load;
	target.addEventListener("message", async event => {
		const message = event.data || {};
		if (message.worker) {
			globalThis.wanixWorker = message.worker;
			const state = wanixCheckpointStateFromWorker(message.worker);
			if (state && typeof load === "function") {
				await load(state, message.worker);
			}
			return;
		}
		if (!isWanixCheckpointSaveState(message)) {
			return;
		}
		await postWanixCheckpointSaveState(message, {
			target,
			save: options.save || globalThis.wanixCheckpointState,
		});
	});
}

export const WanixCheckpoint = Object.freeze({
	protocol: WanixCheckpointProtocol,
	isSaveStateMessage: isWanixCheckpointSaveState,
	response: wanixCheckpointResponse,
	saveStateResponse: wanixCheckpointSaveStateResponse,
	postSaveState: postWanixCheckpointSaveState,
	stateFromWorker: wanixCheckpointStateFromWorker,
	install: installWanixCheckpoint,
});

if (typeof globalThis !== "undefined" && !globalThis.WanixCheckpoint) {
	globalThis.WanixCheckpoint = WanixCheckpoint;
}
