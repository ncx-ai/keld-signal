from keld.config.writer import write_atomic, delete_if_empty


def test_write_creates_file_and_dirs(tmp_path):
    target = tmp_path / "sub" / "settings.json"
    write_atomic(target, '{"a": 1}\n', backup=False)
    assert target.read_text() == '{"a": 1}\n'


def test_backup_made_once(tmp_path):
    target = tmp_path / "settings.json"
    target.write_text("original\n")
    write_atomic(target, "v2\n", backup=True)
    bak = tmp_path / "settings.json.keld.bak"
    assert bak.read_text() == "original\n"
    write_atomic(target, "v3\n", backup=True)
    assert bak.read_text() == "original\n"  # not overwritten


def test_delete_if_empty(tmp_path):
    target = tmp_path / "settings.json"
    target.write_text("{}\n")
    assert delete_if_empty(target, "{}\n") is True
    assert not target.exists()
    other = tmp_path / "x.json"
    other.write_text('{"a":1}\n')
    assert delete_if_empty(other, '{"a": 1}\n') is False
