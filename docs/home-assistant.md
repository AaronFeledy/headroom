# Home Assistant REST Sensor

Use Home Assistant's REST sensor to read the Headroom usage-server API. This is
a plain `configuration.yaml` example. This repository does not contain a Home
Assistant add-on.

The server default `127.0.0.1:7823` only works when Home Assistant runs on the same host namespace as the server. For a Raspberry Pi, NAS, or Docker host, run `usage-server` with a non-loopback bind and a bearer token:

```bash
USAGE_AUTH_TOKEN='replace-with-a-long-random-token' \
  ./usage-server --listen-addr 0.0.0.0:7823
```

Store the Home Assistant header value in `secrets.yaml`, including the `Bearer ` prefix:

```yaml
claude_usage_widget_auth: Bearer replace-with-a-long-random-token
```

## REST Sensors

The server endpoint is `GET /api/v1/usage`. It returns one array entry per
enabled provider, sorted by provider name. With all four providers enabled, the
order is `Claude`, `Codex`, `Cursor`, then `Grok`. `Codex` is the compatible API
name for the provider Headroom displays as ChatGPT, so existing sensor names and
indexes do not change.

The `current` and `weekly` sensors below are unchanged and remain valid: keep any existing configuration exactly as it is. `buckets` is a new, additive array field added alongside them, described in [Returned Fields](#returned-fields).

Copy this into `configuration.yaml` and replace `http://usage-server.local:7823` with your server URL:

```yaml
sensor:
  - platform: rest
    name: "Claude Usage Current"
    unique_id: claude_usage_widget_claude_current
    resource: "http://usage-server.local:7823/api/v1/usage"
    scan_interval: 60
    headers:
      Authorization: !secret claude_usage_widget_auth
    value_template: "{{ value_json[0].current.utilization | round(1) }}"
    unit_of_measurement: "%"
    json_attributes_path: "$[0]"
    json_attributes:
      - provider_name
      - primary_label
      - secondary_label
      - show_secondary
      - subtitle
      - primary_status_text
      - secondary_status_text
      - reauth_command
      - current
      - weekly
      - buckets
      - error
      - needs_reauth
      - is_success

  - platform: rest
    name: "Codex Usage Current"
    unique_id: claude_usage_widget_codex_current
    resource: "http://usage-server.local:7823/api/v1/usage"
    scan_interval: 60
    headers:
      Authorization: !secret claude_usage_widget_auth
    value_template: "{{ value_json[1].current.utilization | round(1) }}"
    unit_of_measurement: "%"
    json_attributes_path: "$[1]"
    json_attributes:
      - provider_name
      - primary_label
      - secondary_label
      - show_secondary
      - subtitle
      - primary_status_text
      - secondary_status_text
      - reauth_command
      - current
      - weekly
      - buckets
      - error
      - needs_reauth
      - is_success

  - platform: rest
    name: "Cursor Usage Current"
    unique_id: claude_usage_widget_cursor_current
    resource: "http://usage-server.local:7823/api/v1/usage"
    scan_interval: 60
    headers:
      Authorization: !secret claude_usage_widget_auth
    value_template: "{{ value_json[2].current.utilization | round(1) }}"
    unit_of_measurement: "%"
    json_attributes_path: "$[2]"
    json_attributes:
      - provider_name
      - primary_label
      - secondary_label
      - show_secondary
      - subtitle
      - primary_status_text
      - secondary_status_text
      - reauth_command
      - current
      - weekly
      - buckets
      - error
      - needs_reauth
      - is_success

  - platform: rest
    name: "Grok Usage Current"
    unique_id: claude_usage_widget_grok_current
    resource: "http://usage-server.local:7823/api/v1/usage"
    scan_interval: 60
    headers:
      Authorization: !secret claude_usage_widget_auth
    value_template: "{{ value_json[3].current.utilization | round(1) }}"
    unit_of_measurement: "%"
    json_attributes_path: "$[3]"
    json_attributes:
      - provider_name
      - primary_label
      - secondary_label
      - show_secondary
      - subtitle
      - primary_status_text
      - secondary_status_text
      - reauth_command
      - current
      - weekly
      - buckets
      - error
      - needs_reauth
      - is_success
```

If you enable only some providers, adjust the array indexes to match the returned `/api/v1/usage` order. To avoid index dependence, you can also create one REST sensor per provider endpoint, such as `/api/v1/usage/Claude`; the JSON paths then start at `value_json.current.utilization` instead of `value_json[0].current.utilization`.

## Returned Fields

Each provider entry has this shape:

```json
{
  "provider_name": "Claude",
  "primary_label": "Current Session",
  "secondary_label": "Weekly",
  "show_secondary": true,
  "subtitle": null,
  "primary_status_text": null,
  "secondary_status_text": null,
  "reauth_command": null,
  "current": { "utilization": 42.5, "resets_at": "2026-07-12T18:00:00Z" },
  "weekly": { "utilization": 17.0, "resets_at": null },
  "buckets": [
    { "id": "session", "label": "Current Session", "utilization": 42.5, "resets_at": "2026-07-12T18:00:00Z", "status_text": null },
    { "id": "weekly", "label": "Weekly", "utilization": 17.0, "resets_at": null, "status_text": null },
    { "id": "weekly_fable", "label": "Fable", "utilization": 5.0, "resets_at": null, "status_text": null },
    { "id": "extra", "label": "Extra usage", "utilization": 12.0, "resets_at": null, "status_text": "120 / 1000 credits" }
  ],
  "error": null,
  "needs_reauth": false,
  "is_success": true,
  "rate_limit_reset_credits": null
}
```

Optional strings and reset timestamps are explicit `null`. `is_success` is true only when `error` is `null`.

ChatGPT (`Codex`) responses may also include
`"rate_limit_reset_credits": {"available_count": 3, "account_fingerprint":
"<64 lowercase hex characters>"}`. This metadata reports how many usage resets
are currently banked and, when available, an account fingerprint used to keep a
manual action bound to the account whose usage was checked. The fingerprint is
otherwise `null`. The complete metadata object is `null` when the count is
unknown or the provider returned an error; zero is a known count. The GET usage
endpoints and Home Assistant sensors only read this metadata and never redeem a
reset. Headroom's desktop has a separate, explicitly confirmed reset action.

`buckets` is always present, is `[]` on error, and lists every usage window a provider reports (typically `session` and `weekly`; model-scoped rows like `weekly_fable`; Cursor `auto` / `api`; and credit meters like `extra` / `on_demand` when the account has them enabled or has non-zero spend). Optional `status_text` overrides the reset line (e.g. credit totals). `current` and `weekly` remain frozen compatibility fields so existing sensors keep working unchanged; Cursor preserves its legacy aggregate values there while exposing separate `auto` and `api` entries in `buckets`.


Usage buckets also expose optional `starts_at` and `detail_text` fields (explicit
`null` when unavailable). `starts_at` is the provider-reported UTC start of the
window ending at `resets_at`; Headroom uses valid starts for pacing and retains
its existing estimates for older responses. `detail_text` contains plain-text
billing context for hover details, without replacing `status_text` or changing
reported utilization. Cursor amounts are identified as plan-wide; Grok prepaid
balances are USD, and per-product percentages remain reported details rather
than independent allowance meters. No additional provider requests are needed.

Grok prepaid units and empty-object zero handling follow the
[official Grok Build billing client](https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-shell/src/extensions/billing.rs).
