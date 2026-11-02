"""LLMGW by Vulpoint.com : Hermes Agent plugin for the LLMGW gateway.

- ``llm_request`` middleware: adds ``X-LLMGW-Session`` (stable pseudonyms for the whole
  conversation) and ``X-LLMGW-Client`` to requests whose base_url is the gateway, never elsewhere;
- ``llm_execution`` middleware: strict mode refuses main-model calls that do not go to the gateway;
- ``pre_api_request`` / ``pre_auxiliary_call`` observers: warn once and report bypasses to
  ``POST /v1/events`` (best-effort, asynchronous, no content);
- ``transform_api_error_classification``: a refusal is final (not retried);
- ``/llmgw``: gateway, user, quotas, usage, the session's pseudonymised values, bypasses.

Settings: ``config.yaml`` next to this file (written by integrations/hermes/install.sh),
key: ``LLMGW_API_KEY`` from the Hermes profile ``.env``.
"""

from __future__ import annotations

from . import core
from .runtime import LLMGWPlugin

PLUGIN: LLMGWPlugin | None = None


def register(ctx) -> None:
    """Entry point called by Hermes plugin discovery (no network here: load time is bounded)."""
    global PLUGIN
    plugin = LLMGWPlugin()
    ctx.register_middleware("llm_request", plugin.llm_request)
    ctx.register_middleware("llm_execution", plugin.llm_execution)
    ctx.register_hook("pre_api_request", plugin.pre_api_request)
    ctx.register_hook("pre_auxiliary_call", plugin.pre_auxiliary_call)
    ctx.register_hook("transform_api_error_classification", plugin.classify_error)
    ctx.register_command(
        "llmgw", plugin.command,
        description="LLMGW : passerelle, quotas, valeurs pseudonymisées de la session, contournements")
    plugin.startup_check()
    PLUGIN = plugin
    core.log.debug("LLMGW : plugin %s chargé (%s)", core.PLUGIN_VERSION, core.CONFIG_FILE)
