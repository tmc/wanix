import * as duplex from "@progrium/duplex";

const systemBundleFilesystems = [
    {id: "taskfs", kind: "taskfs", source: "#task"},
    {id: "wanixfs", kind: "system", source: "#wanix"},
    {id: "termfs", kind: "system", source: "#term"},
    {id: "webfs", kind: "system", source: "#web"},
    {id: "vmfs", kind: "system", source: "#vm"},
    {id: "pipefs", kind: "system", source: "#pipe"},
    {id: "signalfs", kind: "system", source: "#signal"},
    {id: "ramfs", kind: "system", source: "#ramfs"},
    {id: "jsfs", kind: "system", source: "#js"},
];

export class WanixHandle {
    constructor(port) {
        const sess = new duplex.Session(new duplex.PortConn(port));
        this.peer = new duplex.Peer(sess, new duplex.CBORCodec());
        this.logger = () => null;
    }

    async readDir(name) {
        this.logger(`readDir ${name}`);
        return (await this.peer.call("ReadDir", [name])).value;
    }

    async makeDir(name) {
        this.logger(`makeDir ${name}`);
        await this.peer.call("Mkdir", [name]);
    }

    async makeDirAll(name) {
        this.logger(`makeDirAll ${name}`);
        await this.peer.call("MkdirAll", [name]);
    }

    async bind(name, newname) {
        this.logger(`unbind ${name} ${newname}`);
        await this.peer.call("Bind", [name, newname]);
    }

    async bindCowFS(base, overlay, target, whiteout=".wh") {
        this.logger(`bindCowFS ${base} ${overlay} ${target} ${whiteout}`);
        await this.peer.call("BindCowFS", [base, overlay, target, whiteout]);
    }

    async bindMemFS(target) {
        this.logger(`bindMemFS ${target}`);
        await this.peer.call("BindMemFS", [target]);
    }

    async unbind(name, newname) {
        this.logger(`unbind ${name} ${newname}`);
        await this.peer.call("Unbind", [name, newname]);
    }
    
    async readFile(name) {
        this.logger(`readFile ${name}`);
        return (await this.peer.call("ReadFile", [name])).value;
    }

    // not sure if readFile approach is good, but this is an option for now
    async readFile2(name) {
        this.logger(`readFile2 ${name}`);
        const rd = await this.openReadable(name);
        const response = new Response(rd); // cute trick
        return new Uint8Array(await response.arrayBuffer());
    }

    async readText(name) {
        return (new TextDecoder()).decode(await this.readFile(name));
    }

    async waitFor(name, timeoutMs=1000) {
        this.logger(`waitFor ${name} ${timeoutMs}ms`);
        await this.peer.call("WaitFor", [name, timeoutMs]);
    }

    async stat(name) {
        this.logger(`stat ${name}`);
        return (await this.peer.call("Stat", [name])).value;
    }

    async writeFile(name, contents) {
        this.logger(`writeFile ${name} len(${contents.length})`);
        if (typeof contents === "string") {
            contents = (new TextEncoder()).encode(contents);
        }
        return (await this.peer.call("WriteFile", [name, contents])).value;
    }

    async appendFile(name, contents) {
        this.logger(`appendFile ${name} len(${contents.length})`);
        if (typeof contents === "string") {
            contents = (new TextEncoder()).encode(contents);
        }
        return (await this.peer.call("AppendFile", [name, contents])).value;
    }

    async archive(name=".") {
        this.logger(`archive ${name}`);
        return (await this.peer.call("Archive", [name])).value;
    }

    async importArchive(name=".", contents) {
        this.logger(`importArchive ${name} len(${contents.length})`);
        if (typeof contents === "string") {
            contents = (new TextEncoder()).encode(contents);
        }
        return (await this.peer.call("ImportArchive", [name, contents])).value;
    }

    async bundleManifest(filesystems=[]) {
        this.logger(`bundleManifest filesystems(${filesystems.length})`);
        return (await this.peer.call("BundleManifest", [JSON.stringify(filesystems)])).value;
    }

    async bundleVMStates() {
        this.logger(`bundleVMStates`);
        return (await this.peer.call("BundleVMStates", [])).value || [];
    }

    async bundleTaskStates() {
        this.logger(`bundleTaskStates`);
        return (await this.peer.call("BundleTaskStates", [])).value || [];
    }

    async restoreBundleManifest(manifest) {
        this.logger(`restoreBundleManifest`);
        if (typeof manifest !== "string") {
            manifest = JSON.stringify(normalizeBundleManifest(manifest));
        }
        return (await this.peer.call("RestoreBundleManifest", [manifest])).value;
    }

    async exportBundle(filesystems=[]) {
        this.logger(`exportBundle filesystems(${filesystems.length})`);
        const manifest = await this.bundleManifest(filesystems);
        const vmStates = await this.bundleVMStates();
        const taskStates = await this.bundleTaskStates();
        if (vmStates.length) {
            manifest.vms = vmStates.map(({data, ...vm}) => vm);
            attachBundleVMGuests(manifest);
        }
        if (taskStates.length) {
            const tasks = new Map((manifest.tasks || []).map(task => [task.id, task]));
            for (const state of taskStates) {
                const task = tasks.get(state.id);
                if (task && state.state_path) {
                    task.state_path = state.state_path;
                }
            }
        }
        checkBundleRunningTaskStates(manifest, vmStates, taskStates);
        const requests = new Map(filesystems.map(desc => [desc.id, desc]));
        const archives = [];
        for (const desc of manifest.filesystems || []) {
            const request = requests.get(desc.id) || desc;
            const source = bundleArchiveSource(desc, request);
            if (!source) {
                if (request.archive) {
                    throw new Error(`exportBundle: filesystem ${desc.id} missing archive source`);
                }
                continue;
            }
            const target = bundleArchiveTarget(desc, request);
            const filesystemSource = bundleArchiveFilesystemSource(desc, request);
            const archive = {
                id: desc.id,
                source,
                data: await this.archive(source),
            };
            if (target && target !== source) {
                archive.target = target;
            }
            if (filesystemSource && filesystemSource !== target) {
                archive.filesystem_source = filesystemSource;
            }
            archives.push(archive);
        }
        const states = [
            ...vmStates
                .filter(state => state.state_path && state.data)
                .map(state => ({
                    kind: "vm",
                    id: state.id,
                    path: state.state_path,
                    data: state.data,
                })),
            ...taskStates
                .filter(state => state.state_path && state.data)
                .map(state => ({
                    kind: "task",
                    id: state.id,
                    path: state.state_path,
                    data: state.data,
                })),
        ];
        return {manifest, archives, states};
    }

    async importBundle(bundle) {
        this.logger(`importBundle`);
        if (!bundle || typeof bundle !== "object" || !bundle.manifest) {
            throw new Error("importBundle: invalid bundle");
        }
        const manifest = normalizeBundleManifest(bundle.manifest);
        manifest.filesystems = (manifest.filesystems || []).map(desc => ({...desc}));
        const filesystems = manifest.filesystems;
        for (const archive of bundle.archives || []) {
            const desc = filesystems.find(desc => desc.id === archive.id);
            if (!desc) {
                throw new Error(`importBundle: unknown filesystem ${archive.id}`);
            }
            const target = bundleArchiveTarget(desc, archive) || archive.source || desc.source;
            if (!target) {
                throw new Error(`importBundle: filesystem ${archive.id} missing target`);
            }
            const filesystemSource = archive.filesystem_source || target;
            if (desc.source !== filesystemSource) {
                desc.source = filesystemSource;
            }
            await this.importArchive(target, bundleArchiveData(archive.data));
        }
        const cleanup = [];
        try {
            for (const state of bundle.states || []) {
                const target = state.path || state.state_path;
                if (!target) {
                    throw new Error(`importBundle: state ${state.id || ""} missing path`);
                }
                const kind = state.kind || "vm";
                if (kind === "task") {
                    const task = (manifest.tasks || []).find(task => task.id === state.id);
                    if (task && task.state_path !== target) {
                        task.state_path = target;
                    }
                } else {
                    const vm = (manifest.vms || []).find(vm => vm.id === state.id);
                    if (vm && vm.state_path !== target) {
                        vm.state_path = target;
                    }
                }
                await this.writeFile(target, bundleStateData(state.data));
                cleanup.push(target);
            }
            return await this.restoreBundleManifest(manifest);
        } finally {
            for (const target of cleanup.reverse()) {
                try {
                    await this.remove(target);
                } catch {
                    // best effort cleanup
                }
            }
        }
    }

    async rename(oldname, newname) {
        this.logger(`rename ${oldname} ${newname}`);
        await this.peer.call("Rename", [oldname, newname]);
    }

    async copy(oldname, newname) {
        this.logger(`copy ${oldname} ${newname}`);
        await this.peer.call("Copy", [oldname, newname]);
    }

    async remove(name) {
        this.logger(`remove ${name}`);
        await this.peer.call("Remove", [name]);
    }

    async removeAll(name) {
        this.logger(`removeAll ${name}`);
        await this.peer.call("RemoveAll", [name]);
    }

    async truncate(name, size) {
        this.logger(`truncate ${name} ${size}`);
        await this.peer.call("Truncate", [name, size]);
    }

    async create(name) {
        this.logger(`create ${name}`);
        return (await this.peer.call("Create", [name])).value;
    }

    async open(name) {
        this.logger(`open ${name}`);
        return (await this.peer.call("Open", [name])).value;
    }

    async openFile(name, flags, mode) {
        this.logger(`openFile ${name} ${flags} ${mode}`);
        return (await this.peer.call("OpenFile", [name, flags, mode])).value;
    }

    async read(fd, count) {
        this.logger(`read ${fd} ${count}`);
        return (await this.peer.call("Read", [fd, count])).value;
    }

    async write(fd, data) {
        this.logger(`write ${fd} len(${data.length})`);
        return (await this.peer.call("Write", [fd, data])).value;
    }

    async writeAt(fd, data, offset) {
        this.logger(`writeAt ${fd} ${offset}`);
        return (await this.peer.call("WriteAt", [fd, data, offset])).value;
    }

    async close(fd) {
        this.logger(`close ${fd}`);
        return (await this.peer.call("Close", [fd])).value;
    }

    async sync(fd) {
        this.logger(`sync ${fd}`);
        return (await this.peer.call("Sync", [fd])).value;
    }

    async fstat(fd) {
        this.logger(`fstat ${fd}`);
        return (await this.peer.call("Fstat", [fd])).value;
    }

    async lstat(name) {
        this.logger(`lstat ${name}`);
        return (await this.peer.call("Lstat", [name])).value;
    }

    async chmod(name, mode) {
        this.logger(`chmod ${name} ${mode}`);
        await this.peer.call("Chmod", [name, mode]);
    }

    async chown(name, uid, gid) {
        this.logger(`chown ${name} ${uid} ${gid}`);
        await this.peer.call("Chown", [name, uid, gid]);
    }

    async fchmod(fd, mode) {
        this.logger(`fchmod ${fd} ${mode}`);
        await this.peer.call("Fchmod", [fd, mode]);
    }

    async fchown(fd, uid, gid) {
        this.logger(`fchown ${fd} ${uid} ${gid}`);
        await this.peer.call("Fchown", [fd, uid, gid]);
    }

    async ftruncate(fd, length) {
        this.logger(`ftruncate ${fd} ${length}`);
        await this.peer.call("Ftruncate", [fd, length]);
    }

    async readlink(name) {
        this.logger(`readlink ${name}`);
        return (await this.peer.call("Readlink", [name])).value;
    }

    async symlink(oldname, newname) {
        this.logger(`symlink ${oldname} ${newname}`);
        await this.peer.call("Symlink", [oldname, newname]);
    }

    async chtimes(name, atime, mtime) {
        this.logger(`chtimes ${name} ${atime} ${mtime}`);
        await this.peer.call("Chtimes", [name, atime, mtime]);
    }

    async openReadable(name) {
        this.logger(`openReadable ${name}`);
        const fd = await this.open(name);
        return this.readable(fd);
    }

    async openWritable(name) {
        this.logger(`openWritable ${name}`);
        const fd = await this.openFile(name, 1, 0);
        return this.writable(fd);
    }

    writable(fd) {
        const self = this;
        return new WritableStream({
            write(chunk) {
                return self.write(fd, chunk);
            },
        });
    }

    readable(fd) {
        const self = this;
        return new ReadableStream({
            async pull(controller) {
                const data = await self.read(fd, 1024);
                if (data === null) {
                    controller.close();
                }
                controller.enqueue(data);
            },
        });
    }
}

export function bundleFilesystems(archives=[]) {
    return [
        ...archives.map(normalizeBundleFilesystem),
        ...systemBundleFilesystems.map(desc => ({...desc})),
    ];
}

if (typeof window !== "undefined") {
    window["WanixBundleFilesystems"] = bundleFilesystems;
}

function bundleArchiveSource(desc, request) {
    if (!request) {
        return "";
    }
    if (request.archive === true) {
        return desc.source;
    }
    if (typeof request.archive === "string") {
        return request.archive;
    }
    return "";
}

function bundleArchiveTarget(desc, request) {
    if (!request) {
        return "";
    }
    return request.archive_target || request.target || request.restore_source || "";
}

function bundleArchiveFilesystemSource(desc, request) {
    if (!request) {
        return "";
    }
    return request.filesystem_source || "";
}

function bundleArchiveData(data) {
    if (data instanceof Uint8Array || typeof data === "string") {
        return data;
    }
    if (data instanceof ArrayBuffer) {
        return new Uint8Array(data);
    }
    if (ArrayBuffer.isView(data)) {
        return new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
    }
    return data;
}

function bundleStateData(data) {
    return bundleArchiveData(data);
}

function normalizeBundleFilesystem(desc) {
    if (typeof desc === "string") {
        return {id: desc, kind: "memfs", source: desc, archive: true};
    }
    if (!desc.source && desc.archive === undefined) {
        return {...desc};
    }
    return {
        kind: "memfs",
        archive: true,
        ...desc,
    };
}

function normalizeBundleManifest(manifest) {
    const out = {...manifest};
    if (typeof out.created_at === "number") {
        out.created_at = new Date(out.created_at * 1000).toISOString();
    }
    return out;
}

function attachBundleVMGuests(manifest) {
    const filesystems = manifest.filesystems || [];
    for (const vm of manifest.vms || []) {
        const guest = filesystems.find(desc => desc.source === `#vm/${vm.id}/guest`);
        if (guest) {
            vm.guest_fs_id = guest.id;
        }
    }
}

function checkBundleRunningTaskStates(manifest, vmStates, taskStates) {
    const taskStatesByID = new Map((taskStates || [])
        .filter(state => state.state_path && state.data !== undefined && state.data !== null)
        .map(state => [String(state.id), state]));
    const vmStatesByID = new Map((vmStates || [])
        .filter(state => state.state_path && state.data !== undefined && state.data !== null)
        .map(state => [String(state.id), state]));
    for (const task of manifest.tasks || []) {
        if (task.state !== "running") {
            continue;
        }
        const vmID = bundleTaskVMID(task);
        if (vmID && vmStatesByID.has(vmID)) {
            continue;
        }
        if (task.state_path || taskStatesByID.has(String(task.id))) {
            continue;
        }
        throw new Error(`exportBundle: running task ${task.id} missing checkpoint state`);
    }
}

function bundleTaskVMID(task) {
    for (const line of task.env || []) {
        const eq = line.indexOf("=");
        if (eq > 0 && line.slice(0, eq) === "vm") {
            return line.slice(eq + 1);
        }
    }
    return "";
}

// for safari
if (!ReadableStream.prototype[Symbol.asyncIterator]) {
    ReadableStream.prototype[Symbol.asyncIterator] = async function* () {
        const reader = this.getReader();
        try {
            while (true) {
                const { done, value } = await reader.read();
                if (done) return;
                yield value;
            }
        } finally {
            reader.releaseLock();
        }
    };
}
