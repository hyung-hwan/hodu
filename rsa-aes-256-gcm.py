#!/usr/bin/env python3

import base64
import os
import sys

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric import padding
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.serialization import load_pem_private_key
from cryptography.hazmat.primitives.serialization import load_pem_public_key


def fail(msg: str) -> None:
	sys.stderr.write(msg + "\n")
	sys.exit(1)


def usage() -> None:
	fail(
		"USAGE: rsa-aes-256-gcm.py encipher key-file [data]\n"
		"       rsa-aes-256-gcm.py decipher key-file [document]\n\n"
		"If data or document is omitted, stdin is used."
	)


def load_key_text(path: str) -> bytes:
	try:
		return open(path, "rb").read()
	except OSError:
		fail(f"unable to read {path}")


def load_private_key(path: str):
	try:
		return load_pem_private_key(load_key_text(path), password=None)
	except ValueError:
		fail(f"unable to load private key from {path}")


def load_public_key(path: str):
	key_text = load_key_text(path)

	try:
		return load_pem_public_key(key_text)
	except ValueError:
		pass

	try:
		private_key = load_pem_private_key(key_text, password=None)
	except ValueError:
		fail(f"unable to load public key from {path}")

	return private_key.public_key()


def rsa_oaep_sha256_encrypt(public_key, plaintext: bytes) -> bytes:
	if not isinstance(public_key, rsa.RSAPublicKey):
		fail("unable to get rsa public key details")

	try:
		return public_key.encrypt(
			plaintext,
			padding.OAEP(
				mgf=padding.MGF1(algorithm=hashes.SHA256()),
				algorithm=hashes.SHA256(),
				label=None,
			),
		)
	except ValueError:
		fail("rsa encryption failed")


def rsa_oaep_sha256_decrypt(private_key, ciphertext: bytes) -> bytes:
	if not isinstance(private_key, rsa.RSAPrivateKey):
		fail("unable to get rsa private key details")

	try:
		return private_key.decrypt(
			ciphertext,
			padding.OAEP(
				mgf=padding.MGF1(algorithm=hashes.SHA256()),
				algorithm=hashes.SHA256(),
				label=None,
			),
		)
	except ValueError:
		fail("rsa decryption failed")


def encipher(public_key, text: str) -> str:
	aes_key = os.urandom(32)
	nonce = os.urandom(12)
	ciphertext = AESGCM(aes_key).encrypt(nonce, text.encode("utf-8"), None)
	encrypted_key = rsa_oaep_sha256_encrypt(public_key, aes_key)

	return (
		base64.b64encode(encrypted_key).decode("ascii")
		+ "."
		+ base64.b64encode(nonce).decode("ascii")
		+ "."
		+ base64.b64encode(ciphertext).decode("ascii")
	)


def decipher(private_key, doc: str) -> str:
	parts = doc.split(".", 2)

	if len(parts) != 3:
		fail("invalid serialized token document")

	try:
		encrypted_key = base64.b64decode(parts[0], validate=True)
	except Exception:
		fail("invalid encrypted key")

	try:
		nonce = base64.b64decode(parts[1], validate=True)
	except Exception:
		fail("invalid nonce")

	try:
		ciphertext = base64.b64decode(parts[2], validate=True)
	except Exception:
		fail("invalid ciphertext")

	if len(nonce) != 12:
		fail(f"invalid nonce size")
	if len(ciphertext) < 16:
		fail("invalid ciphertext size")

	aes_key = rsa_oaep_sha256_decrypt(private_key, encrypted_key)

	try:
		plaintext = AESGCM(aes_key).decrypt(nonce, ciphertext, None)
	except Exception:
		fail("aes-gcm decryption failed")

	return plaintext.decode("utf-8")


def main() -> None:
	if len(sys.argv) < 3:
		usage()

	mode = sys.argv[1]
	key_file = sys.argv[2]
	if len(sys.argv) >= 4:
		input_text = sys.argv[3]
	else:
		input_text = sys.stdin.read()

	if mode == "encipher":
		public_key = load_public_key(key_file)
		print(encipher(public_key, input_text))
		return

	if mode == "decipher":
		private_key = load_private_key(key_file)
		print(decipher(private_key, input_text))
		return

	usage()


if __name__ == "__main__":
	main()
