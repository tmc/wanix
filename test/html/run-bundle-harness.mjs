#!/usr/bin/env node

import {spawn} from "node:child_process";
import {existsSync} from "node:fs";
import {mkdtemp, rm} from "node:fs/promises";
import {tmpdir} from "node:os";
import {join} from "node:path";
import WebSocket from "ws";

const repoRoot = new URL("../..", import.meta.url);
const baseURL = process.env.WANIX_BASE_URL || "http://127.0.0.1:7071";
const browserPath = process.env.WANIX_BROWSER || defaultBrowserPath();
const timeoutMs = Number(process.env.WANIX_HARNESS_TIMEOUT || 120000);
const pages = [
	"test/html/bundle-restore.html",
	"test/html/bundle-bindgraph-restore.html",
	"test/html/bundle-cowfs-restore.html",
	"test/html/bundle-running-task-restore.html",
	"test/html/bundle-service-worker-restore.html",
	"test/html/bundle-composite-restore.html",
];

let server;
let browser;
let profile;

function defaultBrowserPath() {
	const candidates = [
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"brave-browser",
		"brave",
		"chromium",
		"google-chrome",
	];
	for (const candidate of candidates) {
		if (candidate.startsWith("/") && !existsSync(candidate)) {
			continue;
		}
		return candidate;
	}
	return "brave";
}

async function ensureServer() {
	try {
		await fetchOK(baseURL + "/test/html/bundle-restore.html");
		return;
	} catch {
	}

	server = spawn("go", ["run", "./examples/serve.go"], {
		cwd: repoRoot,
		stdio: ["ignore", "pipe", "pipe"],
	});
	server.stderr.on("data", data => process.stderr.write(data));
	server.stdout.on("data", data => process.stderr.write(data));
	await waitFor(async () => {
		await fetchOK(baseURL + "/test/html/bundle-restore.html");
	}, 15000);
}

function startBrowser(port, userDataDir) {
	const child = spawn(browserPath, [
		"--headless=new",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-default-apps",
		"--mute-audio",
		`--user-data-dir=${userDataDir}`,
		`--remote-debugging-port=${port}`,
		"about:blank",
	], {
		stdio: ["ignore", "pipe", "pipe"],
	});
	child.stderr.on("data", data => process.stderr.write(data));
	child.stdout.on("data", data => process.stderr.write(data));
	return child;
}

async function runHarness(port, page) {
	const target = await fetchJSON(`http://127.0.0.1:${port}/json/new?${encodeURIComponent("about:blank")}`, {
		method: "PUT",
	});
	const cdp = await connect(target.webSocketDebuggerUrl);
	await cdp.call("Runtime.enable");
	await cdp.call("Page.enable");
	await cdp.call("Log.enable");
	await cdp.call("Page.navigate", {url: `${baseURL}/${page}`});

	const deadline = Date.now() + timeoutMs;
	while (Date.now() < deadline) {
		const value = await cdp.evaluate('document.body && document.body.dataset && document.body.dataset.result || ""');
		if (value) {
			cdp.close();
			return {
				page,
				result: JSON.parse(value),
			};
		}
		await sleep(500);
	}

	const events = cdp.events
		.filter(event => event.method === "Runtime.consoleAPICalled" || event.method === "Runtime.exceptionThrown" || event.method === "Log.entryAdded")
		.slice(-25);
	cdp.close();
	return {
		page,
		result: {
			ok: false,
			error: "timeout",
			events,
		},
	};
}

class CDP {
	constructor(ws) {
		this.ws = ws;
		this.nextID = 1;
		this.pending = new Map();
		this.events = [];
		ws.on("message", data => this.handle(data));
	}

	handle(data) {
		const message = JSON.parse(data.toString());
		if (message.id && this.pending.has(message.id)) {
			const {resolve, reject} = this.pending.get(message.id);
			this.pending.delete(message.id);
			if (message.error) {
				reject(new Error(message.error.message || "cdp error"));
			} else {
				resolve(message.result || {});
			}
			return;
		}
		this.events.push(message);
		if (this.events.length > 300) {
			this.events.shift();
		}
	}

	call(method, params = {}) {
		const id = this.nextID++;
		this.ws.send(JSON.stringify({id, method, params}));
		return new Promise((resolve, reject) => {
			this.pending.set(id, {resolve, reject});
		});
	}

	async evaluate(expression) {
		const result = await this.call("Runtime.evaluate", {
			expression,
			returnByValue: true,
		});
		return result.result && result.result.value;
	}

	close() {
		this.ws.close();
	}
}

async function connect(url) {
	const ws = new WebSocket(url);
	await new Promise((resolve, reject) => {
		ws.once("open", resolve);
		ws.once("error", reject);
	});
	return new CDP(ws);
}

async function fetchOK(url) {
	const response = await fetch(url);
	if (!response.ok) {
		throw new Error(`${response.status} ${response.statusText}`);
	}
	return response;
}

async function fetchJSON(url, options) {
	const response = await fetch(url, options);
	if (!response.ok) {
		throw new Error(`${response.status} ${response.statusText}`);
	}
	return response.json();
}

async function waitForJSON(url, timeout) {
	return waitFor(async () => {
		await fetchJSON(url);
	}, timeout);
}

async function waitFor(fn, timeout) {
	const deadline = Date.now() + timeout;
	let last;
	while (Date.now() < deadline) {
		try {
			return await fn();
		} catch (error) {
			last = error;
			await sleep(200);
		}
	}
	throw last || new Error("timeout");
}

function sleep(ms) {
	return new Promise(resolve => setTimeout(resolve, ms));
}

async function main() {
	try {
		await ensureServer();
		profile = await mkdtemp(join(tmpdir(), "wanix-bundle-browser-"));
		const port = 9521 + Math.floor(Math.random() * 200);
		browser = startBrowser(port, profile);
		await waitForJSON(`http://127.0.0.1:${port}/json/version`, 15000);

		const results = [];
		for (const page of pages) {
			const result = await runHarness(port, page);
			results.push(result);
			console.log(JSON.stringify(result));
		}

		const failed = results.filter(result => !result.result.ok);
		if (failed.length) {
			console.error(JSON.stringify({failed}, null, 2));
			process.exitCode = 1;
		}
	} finally {
		if (browser) {
			browser.kill("SIGTERM");
		}
		if (server) {
			server.kill("SIGTERM");
		}
		if (profile) {
			await rm(profile, {recursive: true, force: true});
		}
	}
}

await main();
