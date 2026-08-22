"""Telegram helpers: secrets for Bot API (sendDocument) and h2-bridge send.

Outbound chat text goes through `h2 send telegram` so the bridge applies
parse_mode=HTML and mirrors a copy to concierge. Direct Bot API is only
for capabilities the bridge does not have (sendDocument).
"""

from __future__ import annotations

import html
import os
import shutil
import subprocess
import tempfile
from pathlib import Path

DEFAULT_SECRETS = Path("/home/ubuntu/h2home/.secrets.env")
DEFAULT_H2 = "/usr/local/bin/h2"
DEFAULT_H2_DIR = Path.home() / "h2home"


def load_telegram_creds(secrets_path: str | os.PathLike[str] | None = None) -> tuple[str, str]:
    path = Path(secrets_path or os.environ.get("H2_SECRETS_FILE") or DEFAULT_SECRETS)
    file_vals: dict[str, str] = {}
    if path.is_file():
        for raw in path.read_text().splitlines():
            line = raw.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, value = line.split("=", 1)
            file_vals[key.strip()] = value.strip().strip('"').strip("'")
    token = os.environ.get("TELEGRAM_BOT_TOKEN") or file_vals.get("TELEGRAM_BOT_TOKEN", "")
    chat = os.environ.get("TELEGRAM_CHAT_ID") or file_vals.get("TELEGRAM_CHAT_ID", "")
    return token, chat


def html_esc(value: object) -> str:
    """Escape <, &, > in dynamic values so they cannot break parse_mode=HTML."""
    return html.escape(str(value), quote=False)


def html_attr(value: object) -> str:
    """Escape a value for use inside a double-quoted HTML attribute."""
    return html.escape(str(value), quote=True)


def h2_send_telegram(text: str) -> None:
    """Deliver text to the user chat via the h2 Telegram bridge.

    The bridge sets parse_mode=HTML. Callers should emit HTML tags and
    html_esc() any untrusted interpolation. Raises on a non-zero h2 exit.
    """
    h2 = shutil.which("h2") or DEFAULT_H2
    env = {**os.environ, "H2_DIR": str(DEFAULT_H2_DIR)}
    fd, path = tempfile.mkstemp(prefix="h2-tg-", suffix=".txt")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(text)
        result = subprocess.run(
            [h2, "send", "telegram", "--file", path],
            env=env,
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            err = (result.stderr or result.stdout or "").strip()
            raise RuntimeError(err or f"h2 send telegram failed ({result.returncode})")
    finally:
        try:
            os.unlink(path)
        except OSError:
            pass
