from core import slug


def test_slug():
    assert slug("  Hello   World ") == "hello-world"
