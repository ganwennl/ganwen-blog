import importlib.util
from pathlib import Path
import tempfile
import unittest

from cryptography.exceptions import InvalidTag

spec = importlib.util.spec_from_file_location("backup_crypto", (Path(__file__).resolve().parents[2] / "scripts/backup/bt-backup-crypto.py"))
crypto = importlib.util.module_from_spec(spec)
spec.loader.exec_module(crypto)


class BackupCryptoTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.private, self.public = self.root / "recovery.pem", self.root / "public.pem"
        crypto.keygen(self.private, self.public)
        self.original = self.root / "original"
        self.original.write_bytes(b"private backup content\x00" * 100000)
        self.encrypted = self.root / "encrypted"
        crypto.process(self.original, self.encrypted, self.public)

    def test_roundtrip_across_blocks_and_empty_file(self):
        target = self.root / "restored"
        crypto.process(self.encrypted, target, self.private, decrypt=True)
        self.assertEqual(target.read_bytes(), self.original.read_bytes())
        self.original.write_bytes(b"")
        empty = self.root / "empty-encrypted"
        crypto.process(self.original, empty, self.public)
        restored = self.root / "empty-restored"
        crypto.process(empty, restored, self.private, decrypt=True)
        self.assertEqual(restored.read_bytes(), b"")

    def test_tamper_never_leaves_partial_plaintext(self):
        data = bytearray(self.encrypted.read_bytes())
        data[-25] ^= 1
        self.encrypted.write_bytes(data)
        target = self.root / "restored"
        with self.assertRaises(InvalidTag):
            crypto.process(self.encrypted, target, self.private, decrypt=True)
        self.assertFalse(target.exists())
        self.assertEqual(list(self.root.glob(".backup-crypto-*")), [])

    def test_wrong_key_truncation_and_overwrite_rejected(self):
        other_private, other_public = self.root / "other.pem", self.root / "other-public.pem"
        crypto.keygen(other_private, other_public)
        target = self.root / "restored"
        with self.assertRaises(ValueError):
            crypto.process(self.encrypted, target, other_private, decrypt=True)
        self.assertFalse(target.exists())
        self.encrypted.write_bytes(crypto.MAGIC + b"\x00\x00\x00\x10")
        with self.assertRaises(ValueError):
            crypto.process(self.encrypted, target, self.private, decrypt=True)
        target.write_bytes(b"must remain")
        with self.assertRaises(ValueError):
            crypto.process(self.original, target, self.public)
        self.assertEqual(target.read_bytes(), b"must remain")
        with self.assertRaises(ValueError):
            crypto.keygen(self.private, self.public)


if __name__ == "__main__":
    unittest.main()
