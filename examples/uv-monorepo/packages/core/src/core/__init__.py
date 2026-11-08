"""Shared code: app depends on it."""


def slug(title: str) -> str:
    """Lower-case words joined by dashes."""
    return "-".join(title.lower().split())
