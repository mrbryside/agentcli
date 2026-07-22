---
title: Schema inference
sidebar_position: 2
---

# Schema inference

The typed custom-tool API infers JSON Schema from its input type at agent
initialization.

## Root input

The root input must be:

- a struct, a pointer to a struct, or
- a map whose key type is `string`.

Tool arguments are JSON objects, so scalar and array root types are rejected.

## Supported Go types

| Go type | JSON Schema |
| --- | --- |
| `string` | `string` |
| signed/unsigned integers | `integer` |
| `float32`, `float64` | `number` |
| `bool` | `boolean` |
| slice or array | `array` with inferred `items` |
| struct | closed `object` with inferred properties |
| `map[string]T` | `object` with inferred `additionalProperties` |
| `time.Time` | `string` with `date-time` format |
| interface | unconstrained schema |

Recursive types are rejected unless an explicit schema override is supplied.

## Field names and required fields

`json` tags define model-visible property names:

```go
type input struct {
    Query string `json:"query"`
    Limit int    `json:"limit,omitempty"`
    Debug bool   `json:"-"`
}
```

`query` is required. `limit` is optional because it has `omitempty`. `Debug` is
not exposed. Struct schemas set `additionalProperties: false`, matching strict
argument decoding.

## Constraint tags

The inference layer supports these tags:

| Struct tag | JSON Schema keyword | Example |
| --- | --- | --- |
| `description` | `description` | `description:"Search phrase"` |
| `enum` | `enum` | `enum:"small,medium,large"` |
| `format` | `format` | `format:"uri"` |
| `pattern` | `pattern` | `pattern:"^[a-z0-9-]+$"` |
| `minLength` | `minLength` | `minLength:"1"` |
| `maxLength` | `maxLength` | `maxLength:"200"` |
| `minimum` | `minimum` | `minimum:"0"` |
| `maximum` | `maximum` | `maximum:"100"` |

Example:

```go
type createIssueInput struct {
    Title string `json:"title" description:"Short issue title" minLength:"3" maxLength:"120"`
    Kind  string `json:"kind" description:"Issue category" enum:"bug,feature,task"`
    Score int    `json:"score,omitempty" minimum:"0" maximum:"100"`
}
```

These constraints shape the provider tool schema. Handlers should still
validate business rules because provider implementations and models cannot be
treated as a security boundary.

## Explicit schema override

Use `ToolSchema` for unions, conditional schemas, `$defs`, or constraints not
covered by tags:

```go
agentcli.WithCustomTool(
    "search",
    "Search one data source.",
    search,
    agentcli.ToolSchema(json.RawMessage(`{
      "type": "object",
      "properties": {
        "query": {"type": "string", "minLength": 1},
        "source": {"oneOf": [
          {"const": "local"},
          {"const": "remote"}
        ]}
      },
      "required": ["query", "source"],
      "additionalProperties": false
    }`)),
)
```

The override must itself declare `type: object`. Typed decoding still happens,
so the schema and Go input type must describe compatible JSON.

