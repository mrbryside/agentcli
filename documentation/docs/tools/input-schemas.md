---
title: Input schemas
sidebar_position: 2
---

# Input schemas

AgentCLI uses explicit input schemas for application tools. Go argument structs
are decoded at runtime; their tags do not generate the provider schema.

## ObjectSchema for ordinary tools

```go
type searchSchema struct {
    Query   agentcli.ToolParameter
    Limit   agentcli.ToolParameter
    Include agentcli.ToolParameter
}

schema := agentcli.ObjectSchema(searchSchema{
    Query: agentcli.StringParameter("Search query").
        Required().
        MinLength(1).
        MaxLength(200),
    Limit: agentcli.IntegerParameter("Maximum results").
        Minimum(1).
        Maximum(100),
    Include: agentcli.ArrayParameter(
        "Fields to include",
        agentcli.InputSchema{Type: "string"},
    ),
})
```

`ObjectSchema` creates a closed object with `additionalProperties: false`.
Exported field names become `lower_snake_case`; a `json` tag can override the
property name or exclude a field. Parameters are optional unless
`.Required()` is applied. Use `TryObjectSchema` when a dynamic declaration
should return an error instead of panicking during initialization.

Keep the schema declaration separate from runtime arguments:

```go
type searchArguments struct {
    Query   *string  `json:"query"`
    Limit   *int     `json:"limit"`
    Include []string `json:"include"`
}
```

The `json` tags above control Go decoding only. AgentCLI does not infer
descriptions or constraints from struct tags.

## Parameter descriptions and constraints

| Helper | Schema type |
| --- | --- |
| `StringParameter` | `string` |
| `IntegerParameter` | `integer` |
| `NumberParameter` | `number` |
| `BooleanParameter` | `boolean` |
| `NullParameter` | `null` |
| `ArrayParameter` | `array` with typed items |
| `ObjectParameter` | caller-supplied object schema |
| `Parameter` | description attached to any `InputSchema` |

Chain `.Required()`, `.Optional()`, `.MinLength()`, `.MaxLength()`,
`.Pattern()`, `.Format()`, `.Minimum()`, `.Maximum()`, and `.Items()` where
applicable. They are serialized into each property's provider JSON Schema;
`Required()` is serialized into the containing object's `required` list.
Provider support is not a security boundary, so enforce the same rules in the
handler.

## Advanced schemas

`InputSchema` exposes scalar, object, array, composition, reference, and
annotation keywords. Exact values such as `const`, `enum`, and `default` use
`json.RawMessage`; numeric constraints retain precision with `json.Number`.

Use `Parameter` to describe an advanced schema:

```go
source := agentcli.Parameter(agentcli.InputSchema{
    OneOf: []agentcli.InputSchema{
        {Const: json.RawMessage(`"local"`)},
        {Const: json.RawMessage(`"remote"`)},
    },
}, "Data source")
```

For a vendor extension or future keyword, validate a raw object schema:

```go
schema, err := agentcli.RawInputSchema(json.RawMessage(`{
  "type": "object",
  "properties": {
    "source": {"type": "string", "enum": ["local", "remote"]}
  },
  "required": ["source"],
  "additionalProperties": false,
  "x-application-rule": true
}`))
if err != nil {
    return err
}
```

`RawInputSchema` must still declare `type: object`. It does not decode or
validate handler arguments automatically.

## Strict runtime decoding

```go
var input searchArguments
if err := agentcli.DecodeArguments(raw, &input); err != nil {
    return nil, err
}
```

`DecodeArguments` rejects non-object JSON, unknown fields, multiple JSON
values, and malformed input. It does not replace checks for required values,
paths, lengths, authorization-dependent rules, or cross-field invariants.

See [Custom tools](./custom-tools.md) for the complete declaration and handler
flow.
