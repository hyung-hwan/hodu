import '@xterm/xterm/css/xterm.css';
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { Unicode11Addon } from "@xterm/addon-unicode11";

function utf8ToBase64(str) {
	const bytes = new TextEncoder().encode(str);
	let binary = "";
	for (const byte of bytes) binary += String.fromCharCode(byte);
	return btoa(binary);
}

function base64ToBinary(encoded) {
	return Uint8Array.from(atob(encoded), character => character.charCodeAt(0));
}

window.onload = function() {
	const mode = document.body.dataset.xtMode || "";
	const container = document.getElementById("terminal-view-container");
	const status = document.getElementById("terminal-status");
	const error = document.getElementById("terminal-errmsg");
	const target = document.getElementById("terminal-target");
	const login = document.getElementById("login-container");
	const form = document.getElementById("login-form");
	const username = document.getElementById("username");
	const password = document.getElementById("password");
	const disconnect = document.getElementById("terminal-disconnect");
	const horizontal = document.getElementById("terminal-split-horizontal");
	const vertical = document.getElementById("terminal-split-vertical");
	const parameters = new URLSearchParams(window.location.search);
	const auto_connect = ["1", "true"].includes((parameters.get("autoconnect") || "").toLowerCase());
	const sessions = new Map();
	const decoder = new TextDecoder();
	let socket = null;
	let credentials = ["", ""];
	let next_id = 0;
	let active = null;

	document.getElementById("login-ssh-part").style.display = mode === "ssh" ? "block" : "none";
	document.getElementById("login-pty-part").style.display = mode === "ssh" ? "none" : "block";
	username.disabled = mode !== "ssh";
	password.disabled = mode !== "ssh";

	function endpoint(name, websocket = false) {
		const url = new URL(window.location.href);
		url.pathname = url.pathname.slice(0, url.pathname.lastIndexOf("/") + 1) + name;
		url.search = "";
		url.hash = "";
		if (websocket) url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
		for (const key of ["access-token", ...(mode === "rpty" ? ["client-token"] : [])]) {
			if (parameters.has(key)) url.searchParams.set(key, parameters.get(key));
		}
		return url;
	}

	async function fetch_target() {
		if (mode !== "ssh") return;
		try {
			const response = await fetch(endpoint("session-info"));
			if (!response.ok) throw new Error("Session information: HTTP " + response.status);
			const info = await response.json();
			target.textContent = info["client-peer-name"] || "";
			document.getElementById("login-form-title").textContent = target.textContent;
			if (target.textContent) document.title = target.textContent;
		} catch (failure) {
			error.textContent = failure.message;
		}
	}

	function set_connected(connected) {
		horizontal.disabled = !connected;
		vertical.disabled = !connected;
		disconnect.disabled = !connected;
	}

	function send(session, type, data) {
		if (socket && socket.readyState === WebSocket.OPEN) {
			socket.send(JSON.stringify({session, type, data}));
		}
	}

	function select(pane) {
		if (active) active.element.classList.remove("active");
		active = pane;
		if (pane) pane.element.classList.add("active");
	}

	function fit(pane) {
		if (!pane.element.isConnected || pane.view.clientWidth < 20 || pane.view.clientHeight < 20) return;
		pane.fit.fit();
		const size = pane.term.rows + ":" + pane.term.cols;
		if (pane.open && size !== pane.size) {
			pane.size = size;
			send(pane.id, "size", [String(pane.term.rows), String(pane.term.cols)]);
		}
	}

	function remove(pane) {
		if (pane.open || pane.opening) send(pane.id, "close", []);
		pane.observer.disconnect();
		cancelAnimationFrame(pane.frame);
		pane.term.dispose();
		sessions.delete(pane.id);
		const parent = pane.element.parentElement;
		if (parent && parent.classList.contains("terminal-split")) {
			const sibling = [...parent.children].find(child => child !== pane.element && !child.classList.contains("terminal-divider"));
			sibling.style.flex = parent.style.flex;
			parent.replaceWith(sibling);
		} else {
			pane.element.remove();
		}
		if (active === pane) {
			select(sessions.values().next().value || null);
			if (active) active.term.focus();
		}
	}

	function divider(split, direction) {
		const handle = document.createElement("div");
		handle.className = "terminal-divider " + direction;
		handle.setAttribute("role", "separator");
		handle.setAttribute("aria-orientation", direction === "horizontal" ? "vertical" : "horizontal");
		handle.tabIndex = 0;
		let ratio = 50;
		function resize(value) {
			const first = split.children[0];
			const second = split.children[2];
			ratio = Math.max(10, Math.min(90, value));
			first.style.flex = ratio + " 1 0";
			second.style.flex = (100 - ratio) + " 1 0";
		}
		handle.onpointerdown = event => {
			event.preventDefault();
			handle.setPointerCapture(event.pointerId);
		};
		handle.onpointermove = event => {
			if (!handle.hasPointerCapture(event.pointerId)) return;
			const bounds = split.getBoundingClientRect();
			const offset = direction === "horizontal" ? event.clientX - bounds.left : event.clientY - bounds.top;
			const length = direction === "horizontal" ? bounds.width : bounds.height;
			if (length > 0) resize(offset * 100 / length);
		};
		handle.onpointerup = event => {
			if (handle.hasPointerCapture(event.pointerId)) handle.releasePointerCapture(event.pointerId);
		};
		handle.onkeydown = event => {
			const back = direction === "horizontal" ? "ArrowLeft" : "ArrowUp";
			const forward = direction === "horizontal" ? "ArrowRight" : "ArrowDown";
			if (event.key === back || event.key === forward) {
				event.preventDefault();
				resize(ratio + (event.key === forward ? 5 : -5));
			}
		};
		return handle;
	}

	function create(direction) {
		if (!socket || socket.readyState !== WebSocket.OPEN) return;
		if (sessions.size >= 32) { error.textContent = "Maximum of 32 terminal panels reached"; return; }
		const previous = active;
		const id = String(++next_id);
		const element = document.createElement("div");
		const header = document.createElement("div");
		const label = document.createElement("span");
		const close = document.createElement("button");
		const view = document.createElement("div");
		element.className = "terminal-pane";
		header.className = "terminal-pane-header";
		label.className = "terminal-pane-label";
		close.type = "button";
		close.textContent = "Close";
		close.setAttribute("aria-label", "Close terminal " + id);
		view.className = "terminal-pane-view";
		header.append(label, close);
		element.append(header, view);
		const pane = {
			id, element, label, view,
			term: new Terminal({
				lineHeight: 1.2,
				fontFamily: "Ubuntu Mono, Consolas, SF Mono, courier-new, courier, monospace",
				allowProposedApi: true
			}),
			fit: new FitAddon(),
			open: false,
			opening: true,
			failure: "",
			size: "",
			frame: 0
		};
		pane.term.loadAddon(pane.fit);
		pane.term.loadAddon(new Unicode11Addon());
		pane.term.unicode.activeVersion = "11";
		if (previous && direction) {
			const split = document.createElement("div");
			split.className = "terminal-split";
			split.style.flexDirection = direction === "horizontal" ? "row" : "column";
			split.style.flex = previous.element.style.flex;
			previous.element.replaceWith(split);
			previous.element.style.flex = "1 1 0";
			element.style.flex = "1 1 0";
			split.append(previous.element, divider(split, direction), element);
		} else {
			container.append(element);
		}
		sessions.set(id, pane);
		pane.term.open(view);
		pane.term.onData(data => {
			if (pane.open) send(id, "iov", [utf8ToBase64(data)]);
		});
		pane.term.onBinary(data => {
			if (pane.open) send(id, "iov", [btoa(data)]);
		});
		pane.observer = new ResizeObserver(() => {
			cancelAnimationFrame(pane.frame);
			pane.frame = requestAnimationFrame(() => fit(pane));
		});
		pane.observer.observe(view);
		element.addEventListener("pointerdown", () => select(pane));
		view.addEventListener("focusin", () => select(pane));
		close.onclick = () => remove(pane);
		label.textContent = "Terminal " + id + " — Connecting";
		select(pane);
		pane.term.focus();
		fit(pane);
		send(id, "open", credentials);
	}

	form.onsubmit = event => {
		event.preventDefault();
		if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) return;
		for (const pane of [...sessions.values()]) remove(pane);
		credentials = mode === "ssh" ? [username.value.trim(), password.value] : ["", ""];
		password.value = "";
		login.style.visibility = "hidden";
		error.textContent = "";
		status.textContent = "Connecting...";
		const connection = new WebSocket(endpoint("ws", true));
		let started = false;
		const fail_initial = message => {
			error.textContent = message;
			status.textContent = "Disconnected";
			login.style.visibility = "visible";
			set_connected(false);
			connection.close();
		};
		socket = connection;
		connection.binaryType = "arraybuffer";
		connection.onopen = () => {
			if (socket !== connection) return;
			status.textContent = "Connected";
			set_connected(true);
			create();
		};
		connection.onmessage = event => {
			if (socket !== connection) return;
			try {
				const message = JSON.parse(typeof event.data === "string" ? event.data : decoder.decode(event.data));
				const pane = sessions.get(message.session);
				if (!pane) {
					if (message.type === "error") {
						error.textContent = message.data.join(" ");
						if (!started) fail_initial(error.textContent);
					}
					return;
				}
				if (message.type === "iov") {
					for (const data of message.data) pane.term.write(base64ToBinary(data));
				} else if (message.type === "status") {
					pane.open = message.data[0] === "opened";
					pane.opening = false;
					pane.label.textContent = "Terminal " + pane.id + (pane.open ? " — Connected" : " — Closed") + (pane.failure ? ": " + pane.failure : "");
					if (pane.open) {
						started = true;
						fit(pane);
					} else if (!started) {
						fail_initial(pane.failure || "Unable to open terminal session");
					}
				} else if (message.type === "error") {
					pane.failure = message.data.join(" ");
					pane.label.textContent = "Terminal " + pane.id + " — " + message.data.join(" ");
					if (!started) {
						fail_initial(pane.failure);
					}
				}
			} catch (failure) {
				error.textContent = failure.message;
			}
		};
		connection.onerror = () => {
			if (socket !== connection) return;
			if (!started) fail_initial("WebSocket connection failed");
			else error.textContent = "WebSocket connection failed";
		};
		connection.onclose = () => {
			if (socket !== connection) return;
			socket = null;
			credentials = ["", ""];
			status.textContent = "Disconnected";
			set_connected(false);
			for (const pane of sessions.values()) {
				pane.open = false;
				pane.opening = false;
				pane.label.textContent = "Terminal " + pane.id + " — Disconnected";
			}
			login.style.visibility = "visible";
		};
	};
	horizontal.onclick = () => create("horizontal");
	vertical.onclick = () => create("vertical");
	disconnect.onclick = () => { if (socket) socket.close(); };
	window.addEventListener("pagehide", () => { if (socket) socket.close(); });
	set_connected(false);
	fetch_target();
	if (mode === "ssh" || !auto_connect) login.style.visibility = "visible";
	else form.requestSubmit();
};
