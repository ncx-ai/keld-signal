import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.paths import PATH_TOKEN, rel_within


def test_rel_within_rejects_a_path_outside_the_root():
    assert rel_within("/etc/passwd", "/home/dg/repo", "/home/dg/repo") is None


def test_a_bare_data_file_is_still_a_path_token():
    """PATH_TOKEN recognises a slash-free token only by its extension, and it read that list off
    EXT_LANG — so `README.md` would have stopped being a path the day Markdown stopped being a
    language. It reads `vocab.PATH_EXT` instead: whether a token is a FILE is a wider question
    than what language it is written in."""
    for tok in ("README.md", "config.yaml", "docker-compose.yml", "package.json"):
        assert PATH_TOKEN.search(tok), tok
    assert PATH_TOKEN.search("mask.go") and PATH_TOKEN.search("app/main.py")


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
