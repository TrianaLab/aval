import importlib
import acme_api.client  # imported, not declared


def plugin(name):
    return importlib.import_module(name)
