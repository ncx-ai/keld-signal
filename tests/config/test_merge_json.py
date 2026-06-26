import json

from keld.config.merge import (
    load_json, dump_json, merge_env, remove_section_keys,
    add_claude_hook, remove_hooks_by_command, has_hook_with_command,
)


def test_load_empty():
    assert load_json(None) == {}
    assert load_json("   ") == {}
    assert load_json('{"a": 1}') == {"a": 1}


def test_dump_is_stable_and_newline_terminated():
    assert dump_json({"a": 1}) == '{\n  "a": 1\n}\n'


def test_merge_env_preserves_user_keys():
    obj = {"env": {"MY_VAR": "x"}}
    keys = merge_env(obj, {"OTEL_X": "1"})
    assert obj["env"] == {"MY_VAR": "x", "OTEL_X": "1"}
    assert keys == ["OTEL_X"]


def test_remove_section_keys_and_prune_empty():
    obj = {"env": {"OTEL_X": "1"}}
    remove_section_keys(obj, "env", ["OTEL_X"])
    assert "env" not in obj


def test_add_and_remove_claude_hook_idempotent():
    obj = {}
    add_claude_hook(obj, "SessionStart", "startup", "python3 ~/.keld/keld-context.py; true")
    add_claude_hook(obj, "SessionStart", "startup", "python3 ~/.keld/keld-context.py; true")
    assert len(obj["hooks"]["SessionStart"]) == 1  # idempotent
    assert has_hook_with_command(obj, "keld-context.py")
    remove_hooks_by_command(obj, "keld-context.py")
    assert "hooks" not in obj


def test_remove_hooks_preserves_user_hooks():
    obj = {"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "mine.sh"}]}]}}
    add_claude_hook(obj, "SessionStart", None, "python3 keld-context.py; true")
    remove_hooks_by_command(obj, "keld-context.py")
    assert obj["hooks"]["SessionStart"] == [
        {"hooks": [{"type": "command", "command": "mine.sh"}]}
    ]


def test_remove_hooks_by_command_removes_only_keld_entry():
    # Event contains both a non-Keld hook and a Keld hook; only Keld entry is removed.
    obj = {}
    add_claude_hook(obj, "SessionStart", "user-matcher", "user-script.sh")
    add_claude_hook(obj, "SessionStart", "keld-matcher", "python3 keld-context.py; true")
    assert len(obj["hooks"]["SessionStart"]) == 2

    remove_hooks_by_command(obj, "keld-context.py")

    # Keld entry gone, user entry intact, hooks section still present
    entries = obj["hooks"]["SessionStart"]
    assert len(entries) == 1
    assert entries[0]["matcher"] == "user-matcher"
    assert "keld-context.py" not in json.dumps(entries)
