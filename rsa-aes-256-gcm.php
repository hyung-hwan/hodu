#!/usr/bin/env php
<?php

declare(strict_types=1);

function fail(string $msg): void {
	fwrite(STDERR, $msg . PHP_EOL);
	exit(1);
}

function usage(): void {
	fail(
		"USAGE: rsa-aes-256-gcm.php encipher public-key-file text-to-encipher [ttl-seconds]\n" .
		"       rsa-aes-256-gcm.php decipher private-key-file [document]\n\n" .
		"If document is omitted, stdin is used. ttl-seconds defaults to 30."
	);
}

function load_key_text(string $path): string {
	$text = @file_get_contents($path);
	if ($text === false) {
		fail("unable to read {$path}");
	}
	return $text;
}

function load_private_key(string $path) {
	$key = openssl_pkey_get_private(load_key_text($path));
	if ($key === false) {
		fail("unable to load private key from {$path}");
	}
	return $key;
}

function load_public_key(string $path) {
	$text = load_key_text($path);
	$key = openssl_pkey_get_public($text);
	if ($key !== false) {
		return $key;
	}

	$private_key = openssl_pkey_get_private($text);
	if ($private_key === false) {
		fail("unable to load public key from {$path}");
	}

	$details = openssl_pkey_get_details($private_key);
	if ($details === false || !isset($details["key"])) {
		fail("unable to extract public key from {$path}");
	}

	$key = openssl_pkey_get_public($details["key"]);
	if ($key === false) {
		fail("unable to extract public key from {$path}");
	}

	return $key;
}

function mgf1_sha256(string $seed, int $length): string {
	$output = "";
	$counter = 0;

	while (strlen($output) < $length) {
		$c = pack("N", $counter);
		$output .= hash("sha256", $seed . $c, true);
		$counter++;
	}

	return substr($output, 0, $length);
}

function oaep_encode_sha256(string $msg, int $k): string {
	$label_hash = hash("sha256", "", true);
	$h_len = strlen($label_hash);
	$m_len = strlen($msg);
	$ps_len = $k - $m_len - (2 * $h_len) - 2;

	if ($ps_len < 0) {
		fail("message too long for rsa key");
	}

	$db = $label_hash . str_repeat("\x00", $ps_len) . "\x01" . $msg;
	$seed = random_bytes($h_len);
	$db_mask = mgf1_sha256($seed, $k - $h_len - 1);
	$masked_db = $db ^ $db_mask;
	$seed_mask = mgf1_sha256($masked_db, $h_len);
	$masked_seed = $seed ^ $seed_mask;

	return "\x00" . $masked_seed . $masked_db;
}

function oaep_decode_sha256(string $em, int $k): string {
	$label_hash = hash("sha256", "", true);
	$h_len = strlen($label_hash);
	$em_len = strlen($em);
	$one_pos = -1;

	if ($em_len !== $k || $k < (2 * $h_len) + 2) {
		fail("invalid oaep block size");
	}
	if ($em[0] !== "\x00") {
		fail("invalid oaep prefix");
	}

	$masked_seed = substr($em, 1, $h_len);
	$masked_db = substr($em, 1 + $h_len);
	$seed_mask = mgf1_sha256($masked_db, $h_len);
	$seed = $masked_seed ^ $seed_mask;
	$db_mask = mgf1_sha256($seed, $k - $h_len - 1);
	$db = $masked_db ^ $db_mask;

	if (substr($db, 0, $h_len) !== $label_hash) {
		fail("invalid oaep label hash");
	}

	for ($i = $h_len; $i < strlen($db); $i++) {
		if ($db[$i] === "\x01") {
			$one_pos = $i;
			break;
		}
		if ($db[$i] !== "\x00") {
			fail("invalid oaep padding");
		}
	}

	if ($one_pos < 0) {
		fail("invalid oaep separator");
	}

	return substr($db, $one_pos + 1);
}

function rsa_oaep_sha256_encrypt($public_key, string $plaintext): string {
	$details = openssl_pkey_get_details($public_key);
	$ciphertext = "";
	$ok = false;

	if ($details === false || !isset($details["bits"])) {
		fail("unable to get rsa public key details");
	}

	$encoded = oaep_encode_sha256($plaintext, intdiv((int)$details["bits"] + 7, 8));
	$ok = openssl_public_encrypt($encoded, $ciphertext, $public_key, OPENSSL_NO_PADDING);
	if (!$ok) {
		fail("rsa encryption failed");
	}

	return $ciphertext;
}

function rsa_oaep_sha256_decrypt($private_key, string $ciphertext): string {
	$details = openssl_pkey_get_details($private_key);
	$encoded = "";
	$ok = false;

	if ($details === false || !isset($details["bits"])) {
		fail("unable to get rsa private key details");
	}

	$ok = openssl_private_decrypt($ciphertext, $encoded, $private_key, OPENSSL_NO_PADDING);
	if (!$ok) {
		fail("rsa decryption failed");
	}

	return oaep_decode_sha256($encoded, intdiv((int)$details["bits"] + 7, 8));
}

function encode_url_base64(string $data): string {
	return rtrim(strtr(base64_encode($data), "+/", "-_"), "=");
}

function decode_url_base64(string $data) {
	$padding_len = 0;
	$decoded = "";

	$padding_len = (4 - (strlen($data) % 4)) % 4;
	$decoded = base64_decode(strtr($data . str_repeat("=", $padding_len), "-_", "+/"), true);
	return $decoded;
}

function encipher($public_key, string $text): string {
	$aes_key = random_bytes(32);
	$nonce = random_bytes(12);
	$tag = "";
	$ciphertext = openssl_encrypt($text, "aes-256-gcm", $aes_key, OPENSSL_RAW_DATA, $nonce, $tag, "", 16);

	if ($ciphertext === false) {
		fail("aes-gcm encryption failed");
	}

	$encrypted_key = rsa_oaep_sha256_encrypt($public_key, $aes_key);
	$ciphertext .= $tag;

	return encode_url_base64($encrypted_key) . "." . encode_url_base64($nonce) . "." . encode_url_base64($ciphertext);
}

function decipher($private_key, string $doc): string {
	$parts = explode(".", $doc, 3);

	if (count($parts) !== 3) {
		fail("invalid serialized token document");
	}

	$encrypted_key = decode_url_base64($parts[0]);
	$nonce = decode_url_base64($parts[1]);
	$ciphertext_and_tag = decode_url_base64($parts[2]);

	if ($encrypted_key === false) {
		fail("invalid encrypted key");
	}
	if ($nonce === false) {
		fail("invalid nonce");
	}
	if ($ciphertext_and_tag === false) {
		fail("invalid ciphertext");
	}
	if (strlen($nonce) !== 12) {
		fail("invalid nonce size");
	}
	if (strlen($ciphertext_and_tag) < 16) {
		fail("invalid ciphertext size");
	}

	$aes_key = rsa_oaep_sha256_decrypt($private_key, $encrypted_key);
	$tag = substr($ciphertext_and_tag, -16);
	$ciphertext = substr($ciphertext_and_tag, 0, -16);
	$plaintext = openssl_decrypt($ciphertext, "aes-256-gcm", $aes_key, OPENSSL_RAW_DATA, $nonce, $tag, "");

	if ($plaintext === false) {
		fail("aes-gcm decryption failed");
	}

	return $plaintext;
}

function make_token_payload(string $token, int $now, int $ttl): string {
	return rtrim(strtr(base64_encode($token), "+/", "-_"), "=") . "|" . (string)$now . "|" . (string)($now + $ttl);
}

function parse_token_payload(string $payload): string {
	$parts = explode("|", $payload, 3);
	$token = "";
	$padding_len = 0;

	if (count($parts) !== 3) {
		fail("invalid protected token payload");
	}

	$padding_len = (4 - (strlen($parts[0]) % 4)) % 4;
	$token = base64_decode(strtr($parts[0] . str_repeat("=", $padding_len), "-_", "+/"), true);
	if ($token === false) {
		fail("invalid protected token text");
	}

	return $token . "|" . $parts[1] . "|" . $parts[2];
}

if ($argc < 3) {
	usage();
}

$mode = $argv[1];
$key_file = $argv[2];

if ($mode === "encipher") {
	$input = "";
	$ttl = 30;
	$now = time();
	$public_key = load_public_key($key_file);

	if ($argc >= 4) {
		$input = $argv[3];
	} else {
		fail("missing token");
	}
	if ($argc >= 5) {
		if (!ctype_digit($argv[4])) {
			fail("invalid ttl-seconds");
		}
		$ttl = (int)$argv[4];
	}

	echo encipher($public_key, make_token_payload($input, $now, $ttl)) . PHP_EOL;
	exit(0);
}

if ($mode === "decipher") {
	$input = $argc >= 4 ? $argv[3] : stream_get_contents(STDIN);
	$private_key = load_private_key($key_file);
	if ($input === false) {
		fail("failed to read input");
	}
	echo parse_token_payload(decipher($private_key, $input)) . PHP_EOL;
	exit(0);
}

usage();
