"""Encrypt server backups with a public key; keep the recovery key off-server."""
import argparse
import base64
import json
import os
from pathlib import Path
import struct
import tempfile

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

MAGIC = b"BTBACKUP1\n"
BLOCK = 1024 * 1024


def regular(path):
    path = Path(path)
    if path.is_symlink() or not path.is_file():
        raise ValueError("Expected a regular file")
    return path


def keygen(private_path, public_path):
    private_path, public_path = Path(private_path), Path(public_path)
    if private_path.exists() or public_path.exists():
        raise ValueError("Refusing to replace an existing recovery key")
    key = rsa.generate_private_key(public_exponent=65537, key_size=3072)
    private_path.parent.mkdir(parents=True, exist_ok=True)
    public_path.parent.mkdir(parents=True, exist_ok=True)
    with private_path.open("xb") as f:
        os.chmod(private_path, 0o600)
        f.write(key.private_bytes(serialization.Encoding.PEM,
                                 serialization.PrivateFormat.PKCS8,
                                 serialization.NoEncryption()))
    with public_path.open("xb") as f:
        f.write(key.public_key().public_bytes(serialization.Encoding.PEM,
                                              serialization.PublicFormat.SubjectPublicKeyInfo))


def process(source, target, key_path, decrypt=False):
    source, key_path, target = regular(source), regular(key_path), Path(target)
    if target.exists() or target.is_symlink():
        raise ValueError("Refusing to replace an existing output")
    target.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=".backup-crypto-", dir=target.parent)
    try:
        with os.fdopen(fd, "wb") as output, source.open("rb") as input_file:
            if decrypt:
                private = serialization.load_pem_private_key(key_path.read_bytes(), password=None)
                if input_file.read(len(MAGIC)) != MAGIC:
                    raise ValueError("Unknown backup format")
                size_bytes = input_file.read(4)
                if len(size_bytes) != 4:
                    raise ValueError("Truncated header")
                size = struct.unpack(">I", size_bytes)[0]
                if not 0 < size < 65536:
                    raise ValueError("Invalid header length")
                encoded = input_file.read(size)
                header = json.loads(encoded.decode("utf-8"))
                if header.get("version") != 1 or header.get("cipher") != "AES-256-GCM+RSA-OAEP-SHA256":
                    raise ValueError("Unknown cipher")
                key = private.decrypt(base64.b64decode(header["key"], validate=True),
                                      padding.OAEP(mgf=padding.MGF1(hashes.SHA256()),
                                                   algorithm=hashes.SHA256(), label=None))
                nonce = base64.b64decode(header["nonce"], validate=True)
                if len(key) != 32 or len(nonce) != 12:
                    raise ValueError("Invalid key or nonce")
                start = input_file.tell()
                remaining = source.stat().st_size - start - 16
                if remaining < 0:
                    raise ValueError("Truncated backup")
                input_file.seek(-16, os.SEEK_END)
                tag = input_file.read(16)
                input_file.seek(start)
                worker = Cipher(algorithms.AES(key), modes.GCM(nonce, tag)).decryptor()
                worker.authenticate_additional_data(MAGIC + size_bytes + encoded)
                while remaining:
                    chunk = input_file.read(min(BLOCK, remaining))
                    if not chunk:
                        raise ValueError("Truncated ciphertext")
                    output.write(worker.update(chunk))
                    remaining -= len(chunk)
                output.write(worker.finalize())
            else:
                public_key = serialization.load_pem_public_key(key_path.read_bytes())
                if not isinstance(public_key, rsa.RSAPublicKey) or public_key.key_size < 3072:
                    raise ValueError("Expected an RSA public key of at least 3072 bits")
                key, nonce = os.urandom(32), os.urandom(12)
                wrapped = public_key.encrypt(key, padding.OAEP(mgf=padding.MGF1(hashes.SHA256()),
                                                              algorithm=hashes.SHA256(), label=None))
                encoded = json.dumps({"version": 1, "cipher": "AES-256-GCM+RSA-OAEP-SHA256",
                                      "key": base64.b64encode(wrapped).decode("ascii"),
                                      "nonce": base64.b64encode(nonce).decode("ascii")},
                                     separators=(",", ":")).encode("utf-8")
                prefix = MAGIC + struct.pack(">I", len(encoded)) + encoded
                output.write(prefix)
                worker = Cipher(algorithms.AES(key), modes.GCM(nonce)).encryptor()
                worker.authenticate_additional_data(prefix)
                for chunk in iter(lambda: input_file.read(BLOCK), b""):
                    output.write(worker.update(chunk))
                output.write(worker.finalize())
                output.write(worker.tag)
            output.flush()
            os.fsync(output.fileno())
        # link is atomic and refuses collisions; never expose unauthenticated output.
        os.link(temporary, target)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    gen = sub.add_parser("keygen")
    gen.add_argument("--private-key", required=True)
    gen.add_argument("--public-key", required=True)
    for command in ("encrypt", "decrypt"):
        child = sub.add_parser(command)
        child.add_argument("--input", required=True)
        child.add_argument("--output", required=True)
        child.add_argument("--key", required=True)
    args = parser.parse_args()
    if args.command == "keygen":
        keygen(args.private_key, args.public_key)
    else:
        process(args.input, args.output, args.key, decrypt=args.command == "decrypt")
    print("Completed:", args.command)


if __name__ == "__main__":
    main()
