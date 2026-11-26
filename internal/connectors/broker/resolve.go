package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
)

// resolve checks the tool is one the connector lists and that is not off,
// takes its mode from the admin's modes (never from the connector), and
// checks the arguments are a JSON object of at most MaxArguments bytes.
func (b *Broker) resolve(ctx context.Context, c *call) *sdk.CallToolResult {
	c.mode = connectors.ModeOf(c.conn.ToolModes, c.tool)
	if c.mode == connectors.ToolOff {
		return noSuchTool(c, outcomeDeniedAuthz)
	}
	tools, err := b.upstream(ctx, c.caller, c.conn, c.bearer)
	if err != nil {
		return errorResult(c, outcomeOf(err), upstreamText(err))
	}
	i := slices.IndexFunc(tools, func(t *sdk.Tool) bool { return t.Name == c.tool })
	if i < 0 {
		return noSuchTool(c, outcomeDeniedAuthz)
	}
	c.listed = tools[i]
	args := bytes.TrimSpace(c.args)
	if len(args) == 0 {
		args = []byte("{}")
	}
	var obj map[string]json.RawMessage
	if len(args) > MaxArguments || args[0] != '{' || json.Unmarshal(args, &obj) != nil || !namesOnce(args) {
		return errorResult(c, outcomeToolError, "Give the arguments as a JSON object of at most 64 KiB, each name once.")
	}
	c.args = args
	return nil
}

// namesOnce reports whether data is one JSON value in which no object names
// a field twice. Parsers disagree on which of two values wins, so the policy
// and the connector could each act on a different one; such arguments are
// refused.
func namesOnce(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	if !valueNamesOnce(dec) {
		return false
	}
	_, err := dec.Token()
	return err == io.EOF
}

// valueNamesOnce reads the next value from dec, checking every object in it.
func valueNamesOnce(dec *json.Decoder) bool {
	t, err := dec.Token()
	if err != nil {
		return false
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			k, err := dec.Token()
			name, ok := k.(string)
			if err != nil || !ok || seen[name] {
				return false
			}
			seen[name] = true
			if !valueNamesOnce(dec) {
				return false
			}
		}
	case json.Delim('['):
		for dec.More() {
			if !valueNamesOnce(dec) {
				return false
			}
		}
	default:
		return true
	}
	_, err = dec.Token() // the closing bracket
	return err == nil
}
