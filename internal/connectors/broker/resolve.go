package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"unicode"

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
// a field twice and every name can be seen. Parsers disagree on which of two
// values wins, and some fold case, so two names that differ only by case (or
// by a letter that folds to another, such as the long s) count as the same
// name: the policy and the connector could each act on a different value.
// A name with a control, invisible or direction character could read as
// another name to the person approving. Such arguments are refused.
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
			if err != nil || !ok || !visible(name) || seen[folded(name)] {
				return false
			}
			seen[folded(name)] = true
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

// folded is name with each letter replaced by the smallest letter it folds
// to (unicode.SimpleFold), so names that differ only by case are equal.
func folded(name string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		return least
	}, name)
}

// visible reports whether name holds no control, format (zero-width, text
// direction) or line or paragraph separator character.
func visible(name string) bool {
	return !strings.ContainsFunc(name, func(r rune) bool {
		return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp)
	})
}
