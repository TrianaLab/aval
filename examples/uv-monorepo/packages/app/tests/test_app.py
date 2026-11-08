from app import url


def test_url():
    assert url("Shards and Matrices") == "/posts/shards-and-matrices"
