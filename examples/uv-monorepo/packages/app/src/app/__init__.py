"""The app: builds URLs with core.slug."""

from core import slug


def url(title: str) -> str:
    return "/posts/" + slug(title)
