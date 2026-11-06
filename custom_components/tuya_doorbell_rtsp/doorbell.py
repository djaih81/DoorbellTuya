"""Recognize Tuya doorbell messages without retaining image URLs or keys."""
from __future__ import annotations

import base64
import binascii
import hashlib
import json


class DoorbellMessages:
    """Deduplicate cloud reports and suppress cached messages at startup."""

    def __init__(self):
        self._last = None

    def observe(self, value, *, baseline=False) -> bool:
        if not isinstance(value, str) or not value or len(value) > 65536:
            return False
        fingerprint = hashlib.sha256(value.encode()).digest()
        duplicate = fingerprint == self._last
        self._last = fingerprint
        if baseline or duplicate:
            return False
        try:
            raw = value.strip()
            message = json.loads(base64.b64decode(raw + "=" * (-len(raw) % 4), validate=True))
        except (ValueError, UnicodeError, binascii.Error):
            return False
        return isinstance(message, dict) and message.get("cmd") == "ipc_doorbell"
