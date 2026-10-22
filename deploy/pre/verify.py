import json
import subprocess

compose = ["docker", "compose", "--env-file", ".env"]
config = json.loads(subprocess.check_output(compose + ["config", "--format", "json"]))
service = config["services"]["subpool"]
container = subprocess.check_output(compose + ["ps", "-q", "subpool"], text=True).strip()
runtime = json.loads(subprocess.check_output(["docker", "inspect", container]))[0]
environment = dict(value.split("=", 1) for value in runtime["Config"]["Env"] if "=" in value)
assert runtime["Config"]["Image"] == service["image"], "Deployed image differs"
for key in [
    "SUBPOOL_CODEX_AUTO_RESET",
    "SUBPOOL_AUTO_RESET_TO",
    "SUBPOOL_RESET_NOTICE_ENABLED",
    "SUBPOOL_RESET_NOTICE_MCP_URL",
    "SUBPOOL_RESET_NOTICE_MCP_TOOL",
    "SUBPOOL_RESET_NOTICE_CHANNEL",
    "SUBPOOL_DAILY_REPORT_ENABLED",
    "SUBPOOL_DAILY_REPORT_FROM",
    "SUBPOOL_DAILY_REPORT_TO",
    "SUBPOOL_DAILY_REPORT_TIME",
    "SUBPOOL_DAILY_REPORT_TIMEZONE",
]:
    assert environment.get(key) == str(service["environment"].get(key, "")), key
if environment.get("SUBPOOL_DAILY_REPORT_ENABLED") == "true":
    for key in ["SUBPOOL_DAILY_REPORT_FROM", "SUBPOOL_DAILY_REPORT_TO", "SUBPOOL_RESEND_API_KEY"]:
        assert environment.get(key), key
if environment.get("SUBPOOL_RESET_NOTICE_ENABLED") == "true":
    assert environment.get("SUBPOOL_RESET_NOTICE_MCP_AUTHORIZATION"), "Slack MCP authorization missing"
print("Deployed image and daily report configuration verified.")
