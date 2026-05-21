import * as duplex from "@progrium/duplex";
import { setupDevtools } from "../api/devtools.js";
import { WanixHandle } from "../api/handle.js";
import { WanixElement } from "./base.js";

// text loaders set up by esbuild
import wasmExecGo from "../wasm/wasm_exec.go.js";
import wasmExecTinygo from "../wasm/wasm_exec.tinygo.js";

let instanceID = 0;

const DEFAULT_WASM = new URL('./wanix.wasm', import.meta.url).href;

export class SystemElement extends WanixElement {
    constructor() {
        super();
        instanceID++;
        this.instanceID = instanceID;
        this.isReady = false;
        this.debug = false;
        
        this._ready = new Promise(resolve => this._wasmReady = resolve);
        this._ready.then(async () => {
            await this._setupNamespace("1", "", this.querySelectorAll(':scope > wanix-bind'));
            this.isReady = true;
            if (this.debug) {
                setupDevtools(this);
            }
            this.dispatchEvent(new CustomEvent("ready", {
                bubbles: true
            }));
        });
        this._portWrap = (port) => new duplex.PortConn(port);
        this._root = null;
    }

    _setupNamespace(tid="", baseFS="", bindings=[]) {
        // replaced by wasm
        throw new Error("wasm not ready");
    }

    _openPort(tid="") {
        // replaced by wasm
        throw new Error("wasm not ready");
    }

    _open9P(tid="") {
        // replaced by wasm
        throw new Error("wasm not ready");
    }

    // no tid means the root task
    openHandle(tid) {
        return new WanixHandle(this._openPort(tid));
    }

    async ready() {
        if (this.isReady) {
            return;
        }
        return this._ready;
    }

    async startTask(options={}) {
        await this.ready();
        const kind = options.kind || options.type || "auto";
        const id = (await this.root.readText(["#task", "new", kind].join("/"))).trim();
        const path = ["#task", id].join("/");
        const task = this.openHandle(id);
        if (options.cmd) {
            await this.root.writeFile([path, "cmd"].join("/"), options.cmd);
        }
        if (options.alias) {
            await this.root.writeFile([path, "alias"].join("/"), options.alias);
        }
        if (options.dir || options.wd) {
            await this.root.writeFile([path, "dir"].join("/"), options.dir || options.wd);
        }
        if (options.env) {
            await this.root.writeFile([path, "env"].join("/"), taskEnv(options.env));
        }
        await task.bind(path, "#task/self");
        if (options.term) {
            await this.bindTaskTerminal({id, path, task, alias: options.alias});
        }
        if (options.start !== false) {
            await this.root.writeFile([path, "ctl"].join("/"), "start");
        }
        return {id, path, task, termPath: options.term ? ["#task", options.alias || id, "term"].join("/") : ""};
    }

    async bindTaskTerminal(task) {
        const termID = (await this.root.readText("#term/new")).trim();
        const termPath = ["#term", termID].join("/");
        await this.root.bind(termPath, [task.path, "term"].join("/"));
        if (task.alias) {
            await this.root.bind(termPath, ["#task", task.alias, "term"].join("/"));
        }
        await task.task.bind(termPath, "#task/self/term");
        for (const fd of [0, 1, 2]) {
            await task.task.bind([termPath, "program"].join("/"), [task.path, "fd", String(fd)].join("/"));
        }
        task.termID = termID;
        task.term = termPath;
        return termPath;
    }

    async attachTerminal(host, path, options={}) {
        if (typeof host === "string") {
            host = document.getElementById(host);
        }
        const term = document.createElement("wanix-term");
        term.setAttribute("for", this.id);
        term.setAttribute("path", path);
        for (const [key, value] of Object.entries(options)) {
            if (value === false || value === undefined || value === null) {
                continue;
            }
            const attr = key.replace(/[A-Z]/g, ch => "-" + ch.toLowerCase());
            value === true ? term.setAttribute(attr, "") : term.setAttribute(attr, String(value));
        }
        host.replaceChildren(term);
        await new Promise(resolve => requestAnimationFrame(resolve));
        term._system = this;
        await term.connect();
        return term;
    }

    async startTerminalTask(options={}) {
        const task = await this.startTask({...options, term: true});
        if (options.host) {
            await this.attachTerminal(options.host, task.termPath, options.terminal || {});
        }
        return task;
    }

    async writeTerminal(path, text) {
        const stream = await this.root.openWritable([path, "data"].join("/"));
        const writer = stream.getWriter();
        try {
            await writer.write(new TextEncoder().encode(text));
        } finally {
            await writer.close();
        }
    }

    get stdin() {
        this.root.openWritable("#wanix/stdin/data");
    }

    get root() {
        if (!this._root) {
            this._root = this.openHandle();
        }
        return this._root;
    }

    get wasm() {
        if (this.hasAttribute('wasm')) {
            return new URL(this.getAttribute('wasm'), document.baseURI).href;
        } else {
            return DEFAULT_WASM;
        }
    }

    async load(buffer) {
        const wasmBytes = new Uint8Array(buffer);
        const wasmString = new TextDecoder('utf-8', { ignoreBOM: true, fatal: false }).decode(wasmBytes);

        const execScript = document.createElement('script');
        if (wasmString.includes("asyncify_start_unwind")) {
            if (this.debug) console.log("TinyGo WASM detected");
            execScript.textContent = wasmExecTinygo;
        } else {
            if (this.debug) console.log("Go WASM detected");
            execScript.textContent = wasmExecGo;
        }
        // executes synchronously
        document.head.appendChild(execScript);

        const go = new window.Go();
        go.importObject["wanix"] = {
            getInstanceID: () => {
                return this.instanceID;
            }
        };
        WebAssembly.instantiate(wasmBytes, go.importObject).then(obj => {
            go.run(obj.instance);
        });
    }

    disconnectedCallback() {
        delete window.__wanix[this.instanceID];
    }

    connectedCallback() {
        super.connectedCallback();

        if (!window.__wanix) {
            window.__wanix = {};
        }
        window.__wanix[this.instanceID] = this;

        this.debug = this.hasAttribute('debug');

        this.allowOrigins = (this.getAttribute('allow-origins') || "").split(" ");
        if (this.allowOrigins.length > 0 && this.id) {
            if (this.debug) {
                console.debug("exporting", this.id, "for", this.allowOrigins);
            }
            window.addEventListener("message", async (event) => {
                if (event.data.request != "wanix-import") return;
                if (location.hash.slice(1) != this.id) return;
                if (!this.allowOrigins.includes(event.origin) && !this.allowOrigins.includes("*")) return;
                if (this.debug) {
                    console.debug("import requested for", this.id, "from", event.origin);
                }
                await this._ready;
           
                const p9port = await this._open9P("1");
                event.data.responder.postMessage(p9port, [p9port]);
            });
        }

        fetch(this.wasm)
            .then(r => r.arrayBuffer())
            .then(this.load.bind(this))
            .catch(err => {
                console.error("Failed to load Wanix WASM", err);
                this.dispatchEvent(new CustomEvent("error", {
                    detail: { error: err },
                    bubbles: true
                }));
            });
    }

}

if (typeof window !== "undefined") {
    customElements.define("wanix-system", SystemElement);
}

function taskEnv(env) {
    if (Array.isArray(env)) {
        return env.join("\n");
    }
    if (env && typeof env === "object") {
        return Object.entries(env).map(([key, value]) => `${key}=${value}`).join("\n");
    }
    return String(env || "");
}
