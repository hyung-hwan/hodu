import '@xterm/xterm/css/xterm.css';
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { Unicode11Addon } from "@xterm/addon-unicode11";

function utf8ToBase64(str) {
	const encoder = new TextEncoder();
	const data = encoder.encode(str);
	const binaryString = String.fromCharCode.apply(null, data);
	return btoa(binaryString);
}

function base64ToBinary(b64) {
	const binaryString = atob(b64);
	const bytes = new Uint8Array(binaryString.length);
	let i;

	for (i = 0; i < binaryString.length; i++) {
		bytes[i] = binaryString.charCodeAt(i);
	}
	return bytes;
}

window.onload = function(event) {
	const xt_mode = document.body.dataset.xtMode || "";
	const conn_id = document.body.dataset.connId || "";
	const route_id = document.body.dataset.routeId || "";
	const terminal_target = document.getElementById("terminal-target");
	const terminal_status = document.getElementById("terminal-status");
	const terminal_errmsg = document.getElementById("terminal-errmsg");
	const terminal_view_container = document.getElementById("terminal-view-container");
	const terminal_connect = document.getElementById("terminal-connect");
	const terminal_disconnect = document.getElementById("terminal-disconnect");
	const login_container = document.getElementById("login-container");
	const login_form_title = document.getElementById("login-form-title");
	const login_form = document.getElementById("login-form");
	const login_ssh_part = document.getElementById("login-ssh-part");
	const login_pty_part = document.getElementById("login-pty-part");
	const username_field = document.getElementById("username");
	const password_field = document.getElementById("password");
	const qparams = new URLSearchParams(window.location.search);
	const term = new Terminal({
		lineHeight: 1.2,
		fontFamily: "Ubuntu Mono, Consolas, SF Mono, courier-new, courier, monospace",
		allowProposedApi: true
	});
	const fit_addon = new FitAddon();
	const unicode11_addon = new Unicode11Addon();
	const text_decoder = new TextDecoder();

	void event;

	if (xt_mode == "ssh") {
		login_ssh_part.style.display = "block";
		username_field.disabled = false;
		password_field.disabled = false;
		login_pty_part.style.display = "none";
	} else {
		login_ssh_part.style.display = "none";
		username_field.disabled = true;
		password_field.disabled = true;
		login_pty_part.style.display = "block";
	}

	term.loadAddon(fit_addon);
	term.loadAddon(unicode11_addon);
	term.unicode.activeVersion = "11";
	term.open(terminal_view_container);

	const set_terminal_target = function(name) {
		terminal_target.innerText = name;
		login_form_title.innerText = name;
	};

	const set_terminal_status = function(msg, errmsg) {
		if (msg != null) terminal_status.innerText = msg;
		if (errmsg != null) {
			if (errmsg != "") {
				const d = new Date();
				terminal_errmsg.innerText = "[" + d.toLocaleString() + "] " + errmsg;
			} else {
				terminal_errmsg.innerText = errmsg;
			}
		}
	};

	const adjust_terminal_size_unconnected = function() {
		fit_addon.fit();
	};

	const fetch_session_info = async function() {
		let url = window.location.protocol + "//" + window.location.host;
		let pathname = window.location.pathname;

		const qparams = new URLSearchParams(window.location.search);
		const xparams = new URLSearchParams();

		pathname = pathname.substring(0, pathname.lastIndexOf("/"));
		url += pathname + "/session-info";

		const access_token = qparams.get("access-token");
		if (access_token !== null && access_token != "") xparams.set("access-token", access_token);
		if (xparams.size > 0) url += "?" + xparams.toString();

		try {
			const resp = await fetch(url);
			if (!resp.ok) {
				if (xt_mode == "ssh")
					throw new Error(`HTTP error in getting route(${conn_id},${route_id}) information - status ${resp.status}`);
				else
					throw new Error(`HTTP error in getting session information - status ${resp.status}`);
			}
			const route = await resp.json();
			if ("client-peer-name" in route) {
				set_terminal_target(route["client-peer-name"]);
				document.title = route["client-peer-name"];
			}
		} catch (e) {
			set_terminal_target("");
			document.title = "";
			set_terminal_status(null, e);
		}
	};

	const toggle_login_form = function(visible) {
		if (visible && xt_mode == "ssh") fetch_session_info();
		login_container.style.visibility = (visible ? "visible" : "hidden");
		terminal_disconnect.style.visibility = (visible ? "hidden" : "visible");
		if (visible) {
			if (xt_mode == "ssh") username_field.focus();
			else terminal_connect.focus();
		} else {
			term.focus();
		}
	};

	toggle_login_form(true);
	window.onresize = adjust_terminal_size_unconnected;
	adjust_terminal_size_unconnected();

	login_form.onsubmit = async function(event) {
		let username = "";
		let password = "";
		const prefix = window.location.protocol === "https:" ? "wss://" : "ws://";
		let pathname = window.location.pathname;
		const xparams = new URLSearchParams();
		let url;
		let socket;

		event.preventDefault();
		toggle_login_form(false);

		if (xt_mode == "ssh") {
			username = username_field.value.trim();
			password = password_field.value.trim();
		}

		pathname = pathname.substring(0, pathname.lastIndexOf("/"));
		url = prefix + window.location.host + pathname + "/ws";

		const access_token = qparams.get("access-token");
		if (access_token !== null && access_token != "") xparams.set("access-token", access_token);
		if (xt_mode == "rpty") {
			const client_token = qparams.get("client-token");
			if (client_token !== null && client_token != "") xparams.set("client-token", client_token);
		}
		if (xparams.size > 0) url += "?" + xparams.toString();

		socket = new WebSocket(url);
		socket.binaryType = "arraybuffer";

		set_terminal_status("Connecting...", "");

		const adjust_terminal_size_connected = function() {
			fit_addon.fit();
			if (socket.readyState == 1)
				socket.send(JSON.stringify({ type: "size", data: [term.rows.toString(), term.cols.toString()] }));
		};

		socket.onopen = function() {
			socket.send(JSON.stringify({ type: "open", data: [username, password] }));
		};

		socket.onmessage = function(event) {
			try {
				let event_text;
				let parsed_msg;

				event_text = (typeof event.data === "string") ? event.data : text_decoder.decode(new Uint8Array(event.data));
				parsed_msg = JSON.parse(event_text);
				if (parsed_msg.type == "iov") {
					for (const data of parsed_msg.data) term.write(base64ToBinary(data));
				} else if (parsed_msg.type == "status") {
					if (parsed_msg.data.length >= 1) {
						if (parsed_msg.data[0] == "opened") {
							set_terminal_status("Connected", "");
							adjust_terminal_size_connected();
							term.clear();
						} else if (parsed_msg.data[0] == "closed") {
						}
					}
				} else if (parsed_msg.type == "error") {
					set_terminal_status(null, parsed_msg.data.join(" "));
				}
			} catch (e) {
				set_terminal_status(null, e);
			}
		};

		socket.onerror = function(event) {
			void event;
			set_terminal_status("Disconnected", "");
			toggle_login_form(true);
			window.onresize = adjust_terminal_size_unconnected;
		};

		socket.onclose = function() {
			set_terminal_status("Disconnected", null);
			toggle_login_form(true);
			window.onresize = adjust_terminal_size_unconnected;
		};

		term.onData(function(data) {
			if (socket.readyState == 1)
				socket.send(JSON.stringify({ type: "iov", data: [utf8ToBase64(data)] }));
		});

		window.onresize = adjust_terminal_size_connected;
		terminal_disconnect.onclick = function(event) {
			void event;
			socket.send(JSON.stringify({ type: "close", data: [""] }));
		};
	};
};
