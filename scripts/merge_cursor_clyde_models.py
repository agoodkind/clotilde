#!/usr/bin/env python3
"""Sync Cursor's global state.vscdb with OpenAI-adapter aliases from clyde config.

Reads ~/.config/clyde/config.toml, expands [adapter.families] the same way
internal/adapter/models.go (generateFamilyAliases + buildAlias) does, adds
[adapter.models] keys, then merges any missing ids into Cursor's
applicationUser JSON (aiSettings.userAddedModels + availableDefaultModels2).

Requires Cursor fully quit so state.vscdb is not locked.

Usage:
  ./scripts/merge_cursor_clyde_models.py              # check DB vs config only
  ./scripts/merge_cursor_clyde_models.py --apply      # backup, merge missing, verify
"""

from __future__ import annotations

import argparse
import json
import shutil
import sqlite3
import sys
import tomllib
from datetime import datetime, timezone
from pathlib import Path

APPLICATION_USER_KEY = (
    "src.vs.platform.reactivestorage.browser.reactiveStorageServiceImpl."
    "persistentStorage.applicationUser"
)

THINKING_DEFAULT = "default"


def state_vscdb_path() -> Path:
    return Path.home() / "Library/Application Support/Cursor/User/globalStorage/state.vscdb"


def clyde_config_path() -> Path:
    return Path.home() / ".config/clyde/config.toml"


def build_alias(family: str, effort: str, ctx_suffix: str, thinking: str) -> str:
    """Match adapter.buildAlias in internal/adapter/models.go."""
    parts = ["clyde", family]
    if effort:
        parts.append(effort)
    if ctx_suffix:
        parts.append(ctx_suffix)
    if thinking and thinking != THINKING_DEFAULT:
        parts.extend(["thinking", thinking])
    return "-".join(parts)


def expand_family(slug: str, fam: dict) -> list[str]:
    """Match adapter.generateFamilyAliases loops."""
    efforts = fam.get("efforts") or []
    if not efforts:
        efforts = [""]
    thinking_modes = fam.get("thinking_modes") or []
    if not thinking_modes:
        thinking_modes = [""]
    out: list[str] = []
    for ctx in fam.get("contexts") or []:
        alias_suffix = ctx.get("alias_suffix") or ""
        for eff in efforts:
            for th in thinking_modes:
                out.append(build_alias(slug, eff, alias_suffix, th))
    return out


def expected_aliases_from_clyde_config(config_path: Path) -> list[str]:
    data = tomllib.loads(config_path.read_text(encoding="utf-8"))
    adapter = data.get("adapter") or {}
    families = adapter.get("families") or {}
    names: list[str] = []
    for slug, fam in families.items():
        names.extend(expand_family(slug, fam))
    for name in (adapter.get("models") or {}):
        names.append(name)
    return sorted(set(names))


def load_application_user(conn: sqlite3.Connection) -> dict:
    row = conn.execute(
        "SELECT value FROM ItemTable WHERE key = ?",
        (APPLICATION_USER_KEY,),
    ).fetchone()
    if not row:
        raise SystemExit(
            "error: missing applicationUser row; Cursor version may have renamed this key"
        )
    return json.loads(row[0])


def verify_models(data: dict, expected: list[str]) -> tuple[bool, list[str], list[str]]:
    exp = set(expected)
    ai = data.get("aiSettings") or {}
    uam = set(ai.get("userAddedModels") or [])
    avail = data.get("availableDefaultModels2") or []
    avail_names = {
        m.get("serverModelName")
        for m in avail
        if isinstance(m, dict) and m.get("serverModelName")
    }
    missing_uam = sorted(exp - uam)
    missing_avail = sorted(exp - avail_names)
    ok = not missing_uam and not missing_avail
    return ok, missing_uam, missing_avail


def template_entry(avail: list) -> dict:
    for m in avail:
        if not isinstance(m, dict):
            continue
        sn = m.get("serverModelName") or ""
        if sn.startswith("clyde-") or sn.startswith("clotilde-"):
            return json.loads(json.dumps(m))
    for m in avail:
        if isinstance(m, dict) and m.get("serverModelName"):
            return json.loads(json.dumps(m))
    return {"namedModelSectionIndex": 1}


def merge_models(data: dict, new_ids: list[str]) -> None:
    ai = data.setdefault("aiSettings", {})
    uam = ai.setdefault("userAddedModels", [])
    avail = data.setdefault("availableDefaultModels2", [])

    existing = {
        m.get("serverModelName")
        for m in avail
        if isinstance(m, dict) and m.get("serverModelName")
    }

    t = template_entry(avail)
    for mid in new_ids:
        if mid not in uam:
            uam.append(mid)
        if mid in existing:
            continue
        entry = json.loads(json.dumps(t))
        entry["serverModelName"] = mid
        entry["inputboxShortModelName"] = mid
        avail.append(entry)
        existing.add(mid)


def print_status(
    label: str,
    expected: list[str],
    ok: bool,
    miss_uam: list[str],
    miss_avail: list[str],
) -> None:
    print(f"{label}: {len(expected)} alias(es) from clyde config")
    print(f"  userAddedModels: {len(expected) - len(miss_uam)}/{len(expected)} present")
    print(f"  availableDefaultModels2: {len(expected) - len(miss_avail)}/{len(expected)} present")
    if ok:
        print("  status: OK (all expected ids in both places)")
        return
    print("  status: INCOMPLETE")
    if miss_uam:
        print("  missing userAddedModels:", ", ".join(miss_uam))
    if miss_avail:
        print("  missing serverModelName:", ", ".join(miss_avail))


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Compare or merge clyde adapter aliases into Cursor applicationUser.",
    )
    parser.add_argument(
        "--apply",
        action="store_true",
        help="Backup state.vscdb and merge missing ids (default: check only).",
    )
    parser.add_argument(
        "--db",
        type=Path,
        default=None,
        help="Path to state.vscdb (default: ~/Library/.../state.vscdb).",
    )
    parser.add_argument(
        "--clyde-config",
        type=Path,
        default=None,
        help="Path to clyde config.toml (default: ~/.config/clyde/config.toml).",
    )
    args = parser.parse_args()
    db_path = args.db or state_vscdb_path()
    cfg_path = args.clyde_config or clyde_config_path()

    if not cfg_path.is_file():
        print(f"error: clyde config not found: {cfg_path}", file=sys.stderr)
        raise SystemExit(2)

    expected = expected_aliases_from_clyde_config(cfg_path)
    if not expected:
        print("error: no adapter families/models in clyde config", file=sys.stderr)
        raise SystemExit(2)

    if not db_path.is_file():
        print(f"error: Cursor database not found: {db_path}", file=sys.stderr)
        raise SystemExit(2)

    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    data = load_application_user(conn)
    conn.close()
    ok, miss_uam, miss_avail = verify_models(data, expected)
    print_status("check", expected, ok, miss_uam, miss_avail)

    if not args.apply:
        raise SystemExit(0 if ok else 1)

    if ok:
        print("--apply: nothing to do (already complete)")
        raise SystemExit(0)

    backup = db_path.with_name(
        db_path.name + ".preclydemodels." + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    )
    shutil.copy2(db_path, backup)
    print("backup:", backup)

    conn = sqlite3.connect(str(db_path))
    data = load_application_user(conn)
    merge_models(data, expected)
    payload = json.dumps(data, separators=(",", ":"), ensure_ascii=False)
    conn.execute(
        "UPDATE ItemTable SET value = ? WHERE key = ?",
        (payload, APPLICATION_USER_KEY),
    )
    conn.commit()
    conn.close()
    print("wrote:", APPLICATION_USER_KEY)

    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    data = load_application_user(conn)
    conn.close()
    ok2, miss_uam2, miss_avail2 = verify_models(data, expected)
    print_status("after write", expected, ok2, miss_uam2, miss_avail2)
    if ok2:
        print("verify: OK")
        raise SystemExit(0)
    print("verify: FAIL (restore from backup if needed)", file=sys.stderr)
    raise SystemExit(1)


if __name__ == "__main__":
    main()
