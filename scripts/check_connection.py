"""Read-only connection check. Starts no tasks and writes no state."""

import argparse
import asyncio
from pathlib import Path

from codex_thread_bridge.bridge import Bridge
from codex_thread_bridge.rpc import AppServer
from codex_thread_bridge.server import diagnose

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--socket", type=Path, required=True)
    args = parser.parse_args()
    asyncio.run(diagnose(Bridge(AppServer(args.socket))))
