from pathlib import Path

from keld.tools.base import SetupParams, Plan, ToolStatus, ToolAdapter


def test_setup_params_fields():
    p = SetupParams(endpoint="https://e", ingest_token="t", actor="a@b.co")
    assert p.endpoint == "https://e" and p.ingest_token == "t" and p.actor == "a@b.co"


def test_plan_and_status_construct():
    plan = Plan(name="x", config_path=Path("/tmp/c"), after_text="{}\n",
                managed={"k": 1}, summary=["did a thing"], changed=True)
    assert plan.changed and plan.summary == ["did a thing"]
    st = ToolStatus(name="x", installed=True, configured=False, detail="d")
    assert st.installed and not st.configured


def test_adapter_protocol_runtime_checkable():
    class Fake:
        name = "fake"
        display_name = "Fake"
        def detect(self): return False
        def config_path(self): return Path("/tmp/x")
        def apply(self, current_text, params): ...
        def remove(self, current_text, managed): ...
        def status(self, current_text, managed): ...

    assert isinstance(Fake(), ToolAdapter)


def test_plan_conflict_defaults_none_and_settable():
    from pathlib import Path
    from keld.tools.base import Plan
    p = Plan(name="x", config_path=Path("/tmp/c"), after_text="", managed={},
             summary=[], changed=False)
    assert p.conflict is None
    p2 = Plan(name="x", config_path=Path("/tmp/c"), after_text="", managed={},
              summary=[], changed=False, conflict="boom")
    assert p2.conflict == "boom"
