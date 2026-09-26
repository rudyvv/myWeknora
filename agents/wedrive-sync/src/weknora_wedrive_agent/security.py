from __future__ import annotations

import base64
import ctypes
import ctypes.wintypes
import json
import os
from pathlib import Path

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey


class DATA_BLOB(ctypes.Structure):
    _fields_ = [("cbData", ctypes.wintypes.DWORD), ("pbData", ctypes.c_void_p)]


if os.name == "nt":
    _crypt32 = ctypes.WinDLL("crypt32", use_last_error=True)
    _kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    _crypt_protect = _crypt32.CryptProtectData
    _crypt_protect.argtypes = [
        ctypes.POINTER(DATA_BLOB), ctypes.wintypes.LPCWSTR,
        ctypes.POINTER(DATA_BLOB), ctypes.c_void_p, ctypes.c_void_p,
        ctypes.wintypes.DWORD, ctypes.POINTER(DATA_BLOB),
    ]
    _crypt_protect.restype = ctypes.wintypes.BOOL
    _crypt_unprotect = _crypt32.CryptUnprotectData
    _crypt_unprotect.argtypes = [
        ctypes.POINTER(DATA_BLOB), ctypes.POINTER(ctypes.wintypes.LPWSTR),
        ctypes.POINTER(DATA_BLOB), ctypes.c_void_p, ctypes.c_void_p,
        ctypes.wintypes.DWORD, ctypes.POINTER(DATA_BLOB),
    ]
    _crypt_unprotect.restype = ctypes.wintypes.BOOL
    _local_free = _kernel32.LocalFree
    _local_free.argtypes = [ctypes.c_void_p]
    _local_free.restype = ctypes.c_void_p


def _blob(value: bytes) -> tuple[DATA_BLOB, object]:
    buffer = ctypes.create_string_buffer(value)
    return DATA_BLOB(len(value), ctypes.cast(buffer, ctypes.c_void_p)), buffer


def protect(value: bytes) -> bytes:
    if os.name != "nt":
        raise RuntimeError("the production Agent requires Windows DPAPI")
    source, keepalive = _blob(value)
    target = DATA_BLOB()
    if not _crypt_protect(ctypes.byref(source), None, None, None, None, 0, ctypes.byref(target)):
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        return ctypes.string_at(target.pbData, target.cbData)
    finally:
        _local_free(target.pbData)


def unprotect(value: bytes) -> bytes:
    source, keepalive = _blob(value)
    target = DATA_BLOB()
    if not _crypt_unprotect(ctypes.byref(source), None, None, None, None, 0, ctypes.byref(target)):
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        return ctypes.string_at(target.pbData, target.cbData)
    finally:
        _local_free(target.pbData)


class Identity:
    def __init__(self, root: Path):
        self.root = root
        self.config_path = root / "config.json"
        self.key_path = root / "device.key"
        self.device_id = ""
        self.server = ""
        self.private_key: Ed25519PrivateKey | None = None

    def load(self) -> bool:
        if not self.config_path.exists() or not self.key_path.exists():
            return False
        config = json.loads(self.config_path.read_text(encoding="utf-8"))
        raw = unprotect(base64.b64decode(self.key_path.read_bytes()))
        self.private_key = Ed25519PrivateKey.from_private_bytes(raw)
        self.device_id = config["device_id"]
        self.server = config["server"].rstrip("/")
        return True

    def create(self, server: str, device_id: str, private_key: Ed25519PrivateKey) -> None:
        self.root.mkdir(parents=True, exist_ok=True)
        raw = private_key.private_bytes(serialization.Encoding.Raw, serialization.PrivateFormat.Raw, serialization.NoEncryption())
        self.key_path.write_bytes(base64.b64encode(protect(raw)))
        self.config_path.write_text(json.dumps({"server": server.rstrip("/"), "device_id": device_id}), encoding="utf-8")
        self.server, self.device_id, self.private_key = server.rstrip("/"), device_id, private_key

    @staticmethod
    def public_key(private_key: Ed25519PrivateKey) -> str:
        raw = private_key.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)
        return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()

    def sign(self, method: str, path: str, timestamp: str, body: bytes) -> str:
        import hashlib
        assert self.private_key is not None
        message = f"{method}\n{path}\n{timestamp}\n{hashlib.sha256(body).hexdigest()}".encode()
        return base64.urlsafe_b64encode(self.private_key.sign(message)).rstrip(b"=").decode()
