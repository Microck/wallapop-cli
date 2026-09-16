package mcp

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// chatToolsEnabled gates the four chat tools. They are written and their argv
// mapping is exercised by the tests, but issue #3 has to verify live PubNub
// receive against a second account before an agent is handed a send button:
// a tool that sends into the void is worse than no tool. Flip this to true
// when #3 lands and the chat tools ship; nothing else changes.
const chatToolsEnabled = false

// Tool is one MCP tool: its JSON Schema, and the CLI argv it turns into.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema schema `json:"inputSchema"`

	// build maps validated input to the command line. Only the flags the input
	// actually carries are emitted, so the CLI's own defaults still apply.
	build func(b *argv) `json:"-"`
}

// argv builds one command line and collects the first input error.
func (t Tool) argv(in map[string]any) ([]string, error) {
	if in == nil {
		in = map[string]any{}
	}
	if err := t.InputSchema.validate(in); err != nil {
		return nil, err
	}
	b := &argv{in: in}
	t.build(b)
	if b.err != nil {
		return nil, b.err
	}
	// Positionals go last behind --, so a keyword or a hash that starts with a
	// dash is never read as a flag.
	if len(b.rest) > 0 {
		b.out = append(b.out, "--")
		b.out = append(b.out, b.rest...)
	}
	return b.out, nil
}

// withheld names the commands that will not be tools, with the reason an agent
// gets when it asks for one. Destructive or money-adjacent actions stay in the
// hands of the person at the terminal.
var withheld = []struct{ name, why string }{
	{"item_sold", "marking a listing sold is irreversible on Wallapop; run `wallapop item sold` yourself"},
	{"item_delete", "deleting a listing is irreversible on Wallapop; run `wallapop item delete` yourself"},
	{"item_reserve", "reserving a listing changes what buyers see; run `wallapop item reserve` yourself"},
	{"item_create", "publishing a listing is a seller action; run `wallapop item create` yourself"},
	{"item_edit", "editing a listing is a seller action; run `wallapop item edit` yourself"},
}

// chatTools are the tools chatToolsEnabled gates. Kept as one list so the
// gate, the listing and the refusal message cannot fall out of step.
var chatTools = []Tool{chatListTool, chatShowTool, chatSendTool, chatStartTool}

func listTools() []Tool {
	tools := []Tool{searchTool, itemShowTool, userShowTool, watchCheckTool}
	if chatToolsEnabled {
		tools = append(tools, chatTools...)
	}
	return tools
}

func lookupTool(name string) (Tool, bool) {
	for _, t := range listTools() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Tools

var searchTool = Tool{
	Name:        "search",
	Description: "Search Wallapop listings around a location. Mirrors `wallapop search`; returns {items:[...],next_page}.",
	InputSchema: object(props{
		"keywords":  text("what to search for"),
		"lat":       number("latitude of the search centre; defaults to the profile's location"),
		"lng":       number("longitude of the search centre"),
		"distance":  integer("radius in km"),
		"min_price": integer("minimum price"),
		"max_price": integer("maximum price"),
		"condition": list(oneOf("item condition", "new", "as_good_as_new", "good", "fair", "has_given_it_all")),
		"category":  integer("category id, from `wallapop category list`"),
		"shipping":  flag("only items that can be shipped"),
		"since":     oneOf("listing age", "today", "week", "month"),
		"sort":      oneOf("result order", "relevance", "newest", "price_asc", "price_desc"),
		"filter":    dict("category-specific filters, key=value, as listed by `wallapop search filters`"),
		"limit":     integer("stop after this many items"),
		"pages":     integer("how many result pages to fetch"),
		"next_page": text("continue from a previous response's next_page token"),
	}),
	build: func(b *argv) {
		b.command("search")
		b.number("lat", "--lat")
		b.number("lng", "--lng")
		b.integer("distance", "--distance")
		b.integer("min_price", "--min-price")
		b.integer("max_price", "--max-price")
		b.repeated("condition", "--condition")
		b.integer("category", "--category")
		b.boolean("shipping", "--shipping")
		b.text("since", "--since")
		b.text("sort", "--sort")
		b.pairs("filter", "--filter")
		b.integer("limit", "--limit")
		b.integer("pages", "--pages")
		b.text("next_page", "--next")
		b.positional("keywords", false)
	},
}

var itemShowTool = Tool{
	Name:        "item_show",
	Description: "Show one listing in full, including reserved and sold flags. Mirrors `wallapop item show`.",
	InputSchema: object(props{"item": text("12-character item hash or an es.wallapop.com/item/... URL")}, "item"),
	build: func(b *argv) {
		b.command("item", "show")
		b.positional("item", true)
	},
}

var userShowTool = Tool{
	Name:        "user_show",
	Description: "Show a seller's public profile and stats. Mirrors `wallapop user show`.",
	InputSchema: object(props{"user": text("user hash, profile URL, or web slug (name-12345678)")}, "user"),
	build: func(b *argv) {
		b.command("user", "show")
		b.positional("user", true)
	},
}

var watchCheckTool = Tool{
	Name:        "watch_check",
	Description: "Run the saved watches once and return the events they produced. Mirrors `wallapop watch check`; empty list means nothing changed.",
	InputSchema: object(props{
		"names": list(text("watch name")),
		"all":   flag("run every watch of the profile, due or not"),
	}),
	build: func(b *argv) {
		b.command("watch", "check")
		b.boolean("all", "--all")
		b.positionals("names")
	},
}

var chatListTool = Tool{
	Name:        "chat_list",
	Description: "List conversations, most recent first. Mirrors `wallapop chat list`.",
	InputSchema: object(props{
		"unread":   flag("only conversations with unread messages"),
		"archived": flag("list archived conversations instead"),
		"limit":    integer("how many conversations"),
	}),
	build: func(b *argv) {
		b.command("chat", "list")
		b.boolean("unread", "--unread")
		b.boolean("archived", "--archived")
		b.integer("limit", "--limit")
	},
}

var chatShowTool = Tool{
	Name:        "chat_show",
	Description: "Show a conversation's messages, oldest first. Mirrors `wallapop chat show`; marks it read unless no_mark_read.",
	InputSchema: object(props{
		"conversation": text("conversation hash, a unique prefix of one, or the hash of an item you already talk about"),
		"limit":        integer("how many messages"),
		"no_mark_read": flag("leave the conversation unread"),
	}, "conversation"),
	build: func(b *argv) {
		b.command("chat", "show")
		b.integer("limit", "--limit")
		b.boolean("no_mark_read", "--no-mark-read")
		b.positional("conversation", true)
	},
}

var chatSendTool = Tool{
	Name:        "chat_send",
	Description: "Send a message into an existing conversation. Mirrors `wallapop chat send`.",
	InputSchema: object(props{
		"conversation": text("conversation hash, a unique prefix, or an item hash you already talk about"),
		"text":         text("the message to send"),
	}, "conversation", "text"),
	build: func(b *argv) {
		b.command("chat", "send")
		b.positional("conversation", true)
		b.positional("text", true)
	},
}

var chatStartTool = Tool{
	Name:        "chat_start",
	Description: "Message an item's seller, reusing the conversation about that item when one exists. Mirrors `wallapop chat start`.",
	InputSchema: object(props{
		"item": text("12-character item hash or item URL"),
		"text": text("the first message"),
	}, "item", "text"),
	build: func(b *argv) {
		b.command("chat", "start")
		b.positional("item", true)
		b.positional("text", true)
	},
}

// Schema

type schema struct {
	Type                 string   `json:"type"`
	Properties           props    `json:"properties"`
	Required             []string `json:"required,omitempty"`
	AdditionalProperties bool     `json:"additionalProperties"`
}

type props map[string]property

type property struct {
	Type                 string    `json:"type"`
	Description          string    `json:"description"`
	Enum                 []string  `json:"enum,omitempty"`
	Items                *property `json:"items,omitempty"`
	AdditionalProperties *property `json:"additionalProperties,omitempty"`
}

func object(p props, required ...string) schema {
	return schema{Type: "object", Properties: p, Required: required}
}

func text(desc string) property    { return property{Type: "string", Description: desc} }
func number(desc string) property  { return property{Type: "number", Description: desc} }
func integer(desc string) property { return property{Type: "integer", Description: desc} }
func flag(desc string) property    { return property{Type: "boolean", Description: desc} }

func oneOf(desc string, values ...string) property {
	return property{Type: "string", Description: desc, Enum: values}
}

func list(of property) property {
	return property{Type: "array", Description: of.Description, Items: &of}
}

func dict(desc string) property {
	v := property{Type: "string"}
	return property{Type: "object", Description: desc, AdditionalProperties: &v}
}

// validate rejects unknown and missing keys before anything reaches the CLI,
// so a mistyped argument names itself instead of being silently dropped.
func (s schema) validate(in map[string]any) error {
	var unknown []string
	for k, v := range in {
		if _, ok := s.Properties[k]; !ok {
			unknown = append(unknown, k)
			continue
		}
		// An explicit null is a mistake, not an omission: passing it for a
		// required argument would otherwise reach the CLI as a missing one.
		if v == nil {
			return fmt.Errorf("argument %q must not be null; leave it out instead", k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		known := make([]string, 0, len(s.Properties))
		for k := range s.Properties {
			known = append(known, k)
		}
		sort.Strings(known)
		return fmt.Errorf("unknown argument %s. Accepted: %s", strings.Join(quoteAll(unknown), ", "), strings.Join(known, ", "))
	}
	for _, r := range s.Required {
		if _, ok := in[r]; !ok {
			return fmt.Errorf("argument %q is required", r)
		}
	}
	return nil
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strconv.Quote(s)
	}
	return out
}

// argv builders

type argv struct {
	in   map[string]any
	out  []string
	rest []string
	err  error
}

func (b *argv) command(parts ...string) { b.out = append(b.out, parts...) }

func (b *argv) fail(key string, want string, got any) {
	if b.err == nil {
		b.err = fmt.Errorf("argument %q must be %s, got %T", key, want, got)
	}
}

// value returns the raw input for key, and whether it was given at all.
func (b *argv) value(key string) (any, bool) {
	v, ok := b.in[key]
	return v, ok && v != nil
}

func (b *argv) text(key, flagName string) {
	v, ok := b.value(key)
	if !ok {
		return
	}
	s, ok := v.(string)
	if !ok {
		b.fail(key, "a string", v)
		return
	}
	b.out = append(b.out, flagName, s)
}

func (b *argv) boolean(key, flagName string) {
	v, ok := b.value(key)
	if !ok {
		return
	}
	on, ok := v.(bool)
	if !ok {
		b.fail(key, "a boolean", v)
		return
	}
	if on {
		b.out = append(b.out, flagName)
	}
}

func (b *argv) number(key, flagName string) {
	v, ok := b.value(key)
	if !ok {
		return
	}
	f, ok := v.(float64)
	if !ok {
		b.fail(key, "a number", v)
		return
	}
	b.out = append(b.out, flagName, strconv.FormatFloat(f, 'f', -1, 64))
}

func (b *argv) integer(key, flagName string) {
	v, ok := b.value(key)
	if !ok {
		return
	}
	f, ok := v.(float64)
	if !ok {
		b.fail(key, "an integer", v)
		return
	}
	if f != float64(int64(f)) {
		b.err = fmt.Errorf("argument %q must be a whole number, got %v", key, f)
		return
	}
	b.out = append(b.out, flagName, strconv.FormatInt(int64(f), 10))
}

// repeated emits one flag occurrence per array element, the way the CLI takes
// repeatable flags such as --condition.
func (b *argv) repeated(key, flagName string) {
	for _, s := range b.strings(key) {
		b.out = append(b.out, flagName, s)
	}
}

// pairs emits one --filter key=value per map entry, sorted so the command line
// is stable for the same input.
func (b *argv) pairs(key, flagName string) {
	v, ok := b.value(key)
	if !ok {
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		b.fail(key, "an object of string values", v)
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s, ok := m[k].(string)
		if !ok {
			b.fail(key+"."+k, "a string", m[k])
			return
		}
		b.out = append(b.out, flagName, k+"="+s)
	}
}

func (b *argv) positional(key string, required bool) {
	v, ok := b.value(key)
	if !ok {
		return
	}
	s, ok := v.(string)
	if !ok {
		b.fail(key, "a string", v)
		return
	}
	if s == "" {
		if required {
			b.err = fmt.Errorf("argument %q must not be empty", key)
		}
		return
	}
	b.rest = append(b.rest, s)
}

func (b *argv) positionals(key string) { b.rest = append(b.rest, b.strings(key)...) }

func (b *argv) strings(key string) []string {
	v, ok := b.value(key)
	if !ok {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		b.fail(key, "an array of strings", v)
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, ok := e.(string)
		if !ok {
			b.fail(key, "an array of strings", e)
			return nil
		}
		out = append(out, s)
	}
	return out
}
