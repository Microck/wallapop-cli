package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Microck/wallapop-cli/internal/cli"
	"github.com/Microck/wallapop-cli/internal/fakewallapop"
)

// harness drives the CLI through its entrypoint against a fake Wallapop with
// an isolated XDG home. Every test goes through here; nothing reaches into
// package internals.
type harness struct {
	t    *testing.T
	fake *fakewallapop.Server
	home string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := fakewallapop.New()
	t.Cleanup(fake.Close)
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("HOME", home)
	t.Setenv("WALLAPOP_PROFILE", "")
	t.Setenv("WALLAPOP_SESSION_TOKEN", "")
	t.Setenv("WALLAPOP_CONFIG", "")
	t.Setenv("NO_COLOR", "")
	for k, v := range fake.Env() {
		t.Setenv(k, v)
	}
	return &harness{t: t, fake: fake, home: home}
}

type result struct {
	code   int
	stdout string
	stderr string
}

func (h *harness) run(stdin string, args ...string) result {
	h.t.Helper()
	var out, errb bytes.Buffer
	code := cli.Execute("test", args, strings.NewReader(stdin), &out, &errb)
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func (h *harness) must(stdin string, args ...string) result {
	h.t.Helper()
	r := h.run(stdin, args...)
	if r.code != 0 {
		h.t.Fatalf("wallapop %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r
}

func (h *harness) cookieFile() string {
	p := filepath.Join(h.home, "cookies.txt")
	content := "# Netscape HTTP Cookie File\n" +
		".wallapop.com\tTRUE\t/\tFALSE\t1820917831\tdevice_id\td3c456c7-9f01-45ab-a986-634193eb77c0\n" +
		"#HttpOnly_es.wallapop.com\tFALSE\t/\tTRUE\t1791974737\t__Secure-next-auth.session-token\t" + fakewallapop.ValidCookie + "\n" +
		"es.wallapop.com\tFALSE\t/\tFALSE\t1789382737\tuserConsentSent\ttrue\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func (h *harness) login() {
	h.t.Helper()
	h.must("", "auth", "login", "--cookies", h.cookieFile())
}

func (h *harness) credentialsPath() string {
	return filepath.Join(h.home, "config", "wallapop-cli", "credentials.toml")
}

func decode(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, s)
	}
}

func bike(hash string, price float64) fakewallapop.Item {
	return fakewallapop.Item{Hash: hash, Title: "Bike " + hash, Price: price}
}

// auth

func TestLoginImportsCookieSeedsLocationAndProtectsCredentials(t *testing.T) {
	h := newHarness(t)
	r := h.must("", "auth", "login", "--cookies", h.cookieFile())
	var st struct {
		Profile  string `json:"profile"`
		LoggedIn bool   `json:"logged_in"`
		Location *struct{ Lat, Lng float64 }
	}
	decode(t, r.stdout, &st)
	if !st.LoggedIn || st.Profile != "default" {
		t.Fatalf("unexpected status: %+v", st)
	}
	if st.Location == nil || st.Location.Lat != 41.39 {
		t.Fatalf("location not seeded from account: %+v", st.Location)
	}
	info, err := os.Stat(h.credentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode = %o, want 600", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(h.credentialsPath())
	// The mint rotates the cookie; the newest one must be what is stored.
	if !strings.Contains(string(raw), fakewallapop.RotatedCookie) {
		t.Fatalf("rotated cookie not persisted:\n%s", raw)
	}
	if strings.Contains(string(raw), "userConsentSent") {
		t.Fatal("unrelated cookies must not be stored")
	}
}

func TestLoginAcceptsPastedCookieValueOnStdin(t *testing.T) {
	h := newHarness(t)
	h.must("__Secure-next-auth.session-token="+fakewallapop.ValidCookie+"\n", "auth", "login", "--cookies-stdin")
	r := h.must("", "auth", "status", "--check")
	if !strings.Contains(r.stdout, `"session_valid": true`) {
		t.Fatalf("session should be valid:\n%s", r.stdout)
	}
}

func TestLoginRejectsInvalidSessionWithAuthExit(t *testing.T) {
	h := newHarness(t)
	h.fake.MintEmpty = true
	r := h.run("", "auth", "login", "--cookies", h.cookieFile())
	if r.code != 3 {
		t.Fatalf("exit %d, want 3; stderr: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(h.credentialsPath()); !os.IsNotExist(err) {
		t.Fatal("a rejected session must not be saved")
	}
}

func TestLoginWithoutTerminalOrFlagsIsUsageError(t *testing.T) {
	h := newHarness(t)
	r := h.run("", "auth", "login")
	if r.code != 2 || !strings.Contains(r.stderr, "--cookies") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
}

func TestAuthRequiredCommandsFailWithExit3(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"me", "show"}, {"auth", "refresh"}} {
		if r := h.run("", args...); r.code != 3 {
			t.Fatalf("%v: exit %d, want 3", args, r.code)
		}
	}
	r := h.run("", "--error-format", "json", "chat", "list")
	var env struct {
		Code      int
		Category  string
		Suggested []string `json:"suggested_commands"`
	}
	decode(t, r.stderr, &env)
	if env.Code != 3 || env.Category != "auth" || len(env.Suggested) == 0 {
		t.Fatalf("envelope %+v", env)
	}
}

func TestProfilesAreIndependentAndSwitchable(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.must("", "auth", "login", "--cookies", h.cookieFile(), "--profile", "work")
	r := h.must("", "profile", "list")
	var list []struct {
		Name    string
		Default bool
	}
	decode(t, r.stdout, &list)
	if len(list) != 2 || !list[0].Default || list[0].Name != "default" {
		t.Fatalf("profiles: %+v", list)
	}
	h.must("", "profile", "use", "work")
	r = h.must("", "auth", "status")
	if !strings.Contains(r.stdout, `"profile": "work"`) {
		t.Fatalf("default profile did not switch:\n%s", r.stdout)
	}
	h.must("", "auth", "logout", "--profile", "work")
	r = h.run("", "me", "show", "--profile", "work")
	if r.code != 3 {
		t.Fatalf("logged-out profile should be exit 3, got %d", r.code)
	}
}

// search

func TestSearchMapsFlagsToWallapopQueryAndPaginates(t *testing.T) {
	h := newHarness(t)
	h.login()
	for i := 0; i < 41; i++ {
		h.fake.AddItem(bike("hash"+padHash(i), float64(100+i)))
	}
	r := h.must("", "search", "bici", "roja", "--max-price", "500", "--min-price", "10", "--condition", "good", "--since", "week", "--sort", "newest", "--category", "17000", "--shipping", "--distance", "25", "--pages", "2")
	var page struct {
		Items    []struct{ Hash string }
		NextPage string `json:"next_page"`
	}
	decode(t, r.stdout, &page)
	if len(page.Items) != 41 || page.NextPage != "" {
		t.Fatalf("got %d items, next %q", len(page.Items), page.NextPage)
	}
	reqs := h.fake.RequestsTo("/api/v3/search")
	if len(reqs) != 2 {
		t.Fatalf("expected 2 search requests, got %d", len(reqs))
	}
	q := reqs[0].Query
	want := map[string]string{"keywords": "bici roja", "max_sale_price": "500", "min_sale_price": "10", "condition": "good", "time_filter": "lastWeek",
		"order_by": "newest", "category_id": "17000", "is_shippable": "true", "distance_in_km": "25", "latitude": "41.39", "longitude": "2.17", "source": "search_box"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("query %s = %q, want %q", k, q.Get(k), v)
		}
	}
	if reqs[1].Query.Get("next_page") != "page2" {
		t.Errorf("second page did not pass next_page token: %v", reqs[1].Query)
	}
	if reqs[0].UA != "wallapop-cli/test (+https://github.com/Microck/wallapop-cli)" {
		t.Errorf("user agent %q", reqs[0].UA)
	}
}

// padHash makes an 8-character suffix so "hash"+padHash(i) is a valid 12-char hash.
func padHash(i int) string { return fmt.Sprintf("%08d", i) }

func TestSearchWithoutLocationIsUsageError(t *testing.T) {
	h := newHarness(t)
	r := h.run("", "search", "bici")
	if r.code != 2 || !strings.Contains(r.stderr, "--lat") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	h.fake.AddItem(bike("hashaaaaaaaa", 10))
	h.must("", "search", "bici", "--lat", "40.4", "--lng", "-3.7")
}

func TestFilterKeysAndValuesAreValidatedAgainstWallapop(t *testing.T) {
	h := newHarness(t)
	h.login()
	r := h.run("", "search", "coche", "--category", "100", "--filter", "nope=1")
	if r.code != 2 || !strings.Contains(r.stderr, "brand") || !strings.Contains(r.stderr, "min_km") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	r = h.run("", "search", "coche", "--category", "100", "--filter", "brand=Ferrari")
	if r.code != 2 || !strings.Contains(r.stderr, "Toyota") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	h.must("", "search", "coche", "--category", "100", "--filter", "brand=Toyota", "--filter", "max_km=100000")
	q := h.fake.RequestsTo("/api/v3/search")[0].Query
	if q.Get("brand") != "Toyota" || q.Get("max_km") != "100000" {
		t.Fatalf("vertical filters not sent: %v", q)
	}
	if len(h.fake.RequestsTo("/api/v3/search/filters/regular-filters")) != 3 {
		t.Fatalf("filters endpoint should be consulted once per invocation")
	}
}

// items and users

func TestItemShowByHashAndByURLAgree(t *testing.T) {
	h := newHarness(t)
	it := h.fake.AddItem(fakewallapop.Item{Hash: "hashbbbbbbbb", Title: "Trek Marlin", Price: 300, Reserved: true})
	byHash := h.must("", "item", "show", it.Hash)
	byURL := h.must("", "item", "show", "https://es.wallapop.com/item/"+it.Slug)
	var a, b struct {
		Hash      string
		Reserved  bool
		Condition string
		Category  string
		URL       string
	}
	decode(t, byHash.stdout, &a)
	decode(t, byURL.stdout, &b)
	if a != b || !a.Reserved || a.Condition != "good" || a.Category != "Bikes > MTB" {
		t.Fatalf("hash: %+v\nurl:  %+v", a, b)
	}
	r := h.run("", "item", "show", "1301645280")
	if r.code != 2 {
		t.Fatalf("numeric id should be a usage error, got %d: %s", r.code, r.stderr)
	}
	r = h.run("", "--error-format", "json", "item", "show", "zzzzzzzzzzzz")
	if r.code != 4 || !strings.Contains(r.stderr, `"category":"not_found"`) {
		t.Fatalf("exit %d stderr %s", r.code, r.stderr)
	}
}

func TestFavoriteToggleHitsWallapop(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(bike("hashcccccccc", 50))
	h.must("", "item", "favorite", it.Hash)
	r := h.must("", "me", "favorites")
	if !strings.Contains(r.stdout, it.Hash) {
		t.Fatalf("item not in favorites:\n%s", r.stdout)
	}
	h.must("", "item", "unfavorite", it.Hash)
	r = h.must("", "me", "favorites")
	if strings.Contains(r.stdout, it.Hash) {
		t.Fatal("item still in favorites")
	}
}

func TestDestructiveItemActionsNeedYesWhenNotInteractive(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(fakewallapop.Item{Hash: "hashdddddddd", Title: "Mine", Price: 5, Seller: fakewallapop.UserHash})
	r := h.run("", "item", "delete", it.Hash)
	if r.code != 2 || !strings.Contains(r.stderr, "--yes") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	for _, rq := range h.fake.RequestsUnder("/api/v3/items/" + it.Hash) {
		if rq.Method == "DELETE" {
			t.Fatal("DELETE sent without confirmation")
		}
	}
	h.must("", "item", "sold", it.Hash, "--yes")
	if r := h.must("", "item", "delete", it.Hash, "--yes"); !strings.Contains(r.stdout, `"action": "deleted"`) {
		t.Fatalf("delete should report past tense like the other actions: %s", r.stdout)
	}
	if r := h.run("", "item", "show", it.Hash); r.code != 4 {
		t.Fatalf("deleted item should be gone, exit %d", r.code)
	}
}

func TestUserBySlugURLAndHash(t *testing.T) {
	h := newHarness(t)
	h.fake.AddItem(bike("hasheeeeeeee", 20))
	a := h.must("", "user", "show", fakewallapop.OtherHash)
	b := h.must("", "user", "show", "https://es.wallapop.com/user/other-seller-68037934")
	if a.stdout != b.stdout {
		t.Fatalf("hash and url lookups differ:\n%s\n%s", a.stdout, b.stdout)
	}
	if !strings.Contains(a.stdout, `"rating_average": 4.9`) || !strings.Contains(a.stdout, `"sold": 36`) {
		t.Fatalf("stats missing:\n%s", a.stdout)
	}
	items := h.must("", "user", "items", fakewallapop.OtherHash)
	if !strings.Contains(items.stdout, "hasheeeeeeee") {
		t.Fatal("seller items missing")
	}
	reviews := h.must("", "user", "reviews", fakewallapop.OtherHash)
	if !strings.Contains(reviews.stdout, `"score": 5`) {
		t.Fatalf("reviews:\n%s", reviews.stdout)
	}
}

// chat

func TestChatSendPublishesWebShapedPayload(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(bike("hashffffffff", 80))
	h.fake.AddConversation(fakewallapop.Conversation{Hash: "convhash0001", Item: it.Hash, Other: fakewallapop.OtherHash})
	r := h.must("hola desde stdin\n", "chat", "send", "convh", "-")
	var sent struct {
		Conversation string
		MessageID    string `json:"message_id"`
	}
	decode(t, r.stdout, &sent)
	if sent.Conversation != "convhash0001" || sent.MessageID == "" {
		t.Fatalf("sent: %+v", sent)
	}
	if len(h.fake.Published) != 1 {
		t.Fatalf("published %d messages", len(h.fake.Published))
	}
	p := h.fake.Published[0]
	if p.Channel != "chat."+fakewallapop.OtherHash+"."+it.Hash+"."+fakewallapop.UserHash || p.UUID != fakewallapop.UserHash {
		t.Fatalf("channel %q uuid %q", p.Channel, p.UUID)
	}
	if p.Message["payload"].(map[string]any)["text"] != "hola desde stdin" || p.Message["id"] != sent.MessageID {
		t.Fatalf("message %v", p.Message)
	}
	if p.Meta["type"] != "text" || p.Meta["conversation_hash"] != "convhash0001" || p.Meta["from_user_hash"] != fakewallapop.UserHash || p.Meta["to_user_hash"] != fakewallapop.OtherHash {
		t.Fatalf("meta %v", p.Meta)
	}
}

func TestChatStartReusesExistingConversationOrCreatesOne(t *testing.T) {
	h := newHarness(t)
	h.login()
	known := h.fake.AddItem(bike("hashgggggggg", 10))
	fresh := h.fake.AddItem(bike("hashhhhhhhhh", 20))
	h.fake.AddConversation(fakewallapop.Conversation{Hash: "convhash0002", Item: known.Hash})

	h.must("", "chat", "start", known.Hash, "sigue disponible?")
	if h.fake.CreateCalls != 0 {
		t.Fatal("must reuse the existing conversation")
	}
	r := h.must("", "chat", "start", "https://es.wallapop.com/item/"+fresh.Slug, "hola")
	if h.fake.CreateCalls != 1 || !strings.Contains(r.stdout, "newconv00001") {
		t.Fatalf("create calls %d stdout %s", h.fake.CreateCalls, r.stdout)
	}
	if len(h.fake.Published) != 2 || h.fake.Published[1].Meta["conversation_hash"] != "newconv00001" {
		t.Fatalf("published %v", h.fake.Published)
	}
	// The new conversation is now visible by item hash and by prefix.
	show := h.must("", "chat", "show", fresh.Hash)
	if !strings.Contains(show.stdout, `"text": "hola"`) {
		t.Fatalf("show:\n%s", show.stdout)
	}
}

func TestChatShowMarksReadUnlessOptedOut(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(bike("hashiiiiiiii", 10))
	h.fake.AddConversation(fakewallapop.Conversation{Hash: "convhash0003", Item: it.Hash, Unread: 1, Messages: []fakewallapop.Message{
		{ID: "m1", FromSelf: true, Text: "hola", At: time.Now().Add(-2 * time.Hour), TimeToken: "17000000000000001"},
		{ID: "m2", FromSelf: false, Text: "si", At: time.Now().Add(-time.Hour), TimeToken: "17000000000000002"},
	}})
	h.must("", "chat", "show", "convhash0003", "--no-mark-read")
	if len(h.fake.Actions) != 0 {
		t.Fatal("--no-mark-read must not send a message action")
	}
	r := h.must("", "chat", "show", "convhash0003")
	if len(h.fake.Actions) != 1 || h.fake.Actions[0]["type"] != "seen" || !strings.HasSuffix(h.fake.Actions[0]["path"].(string), "/message/17000000000000002") {
		t.Fatalf("actions %v", h.fake.Actions)
	}
	var conv struct{ Messages []struct{ Text string } }
	decode(t, r.stdout, &conv)
	if len(conv.Messages) != 2 || conv.Messages[0].Text != "hola" {
		t.Fatalf("messages must be oldest first: %+v", conv.Messages)
	}
}

func TestChatOpenStreamsIncomingAndSendsTypedLines(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(bike("hashjjjjjjjj", 10))
	h.fake.AddConversation(fakewallapop.Conversation{Hash: "convhash0001", Item: it.Hash})
	// stdin: one line to send, then a pause is not possible without a TTY, so
	// send and quit; the fake delivers one incoming message on the second poll.
	stdin := "buenas\n/quit\n"
	deadline := time.After(10 * time.Second)
	done := make(chan result)
	go func() { done <- h.run(stdin, "chat", "open", "convhash0001", "--format", "jsonl") }()
	select {
	case r := <-done:
		if r.code != 0 {
			t.Fatalf("exit %d stderr %s", r.code, r.stderr)
		}
		if len(h.fake.Published) != 1 || h.fake.Published[0].Message["payload"].(map[string]any)["text"] != "buenas" {
			t.Fatalf("typed line not sent: %v", h.fake.Published)
		}
	case <-deadline:
		t.Fatal("chat open did not exit on /quit")
	}
}

func TestChatArchiveTreatsConflictAsSuccess(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(bike("hashkkkkkkkk", 10))
	h.fake.AddConversation(fakewallapop.Conversation{Hash: "convhash0004", Item: it.Hash})
	h.must("", "chat", "archive", "convhash0004")
	h.must("", "chat", "archive", "convhash0004") // 409 from the fake
	r := h.must("", "chat", "list", "--archived")
	if !strings.Contains(r.stdout, "convhash0004") {
		t.Fatal("conversation should be archived")
	}
	h.must("", "chat", "archive", "convhash0004", "--undo")
}

// watches

func TestSearchWatchReportsNewItemsAndPriceChangesOnce(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.fake.AddItem(bike("hashllllllll", 100))
	r := h.must("", "watch", "add", "search", "bici", "--name", "bici", "--interval", "30s")
	if !strings.Contains(r.stdout, `"seen": 1`) {
		t.Fatalf("baseline should record the existing item:\n%s", r.stdout)
	}
	h.fake.AddItem(bike("hashmmmmmmmm", 200))
	h.fake.Items["hashllllllll"].Price = 90
	h.fake.Items["hashllllllll"].Reserved = true

	r = h.must("", "watch", "check", "bici", "--format", "jsonl")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 events, got %d:\n%s", len(lines), r.stdout)
	}
	types := map[string]bool{}
	for _, l := range lines {
		var ev struct {
			Type   string
			Watch  string
			Change *struct{ From, To float64 }
		}
		decode(t, l, &ev)
		types[ev.Type] = true
		if ev.Watch != "bici" {
			t.Errorf("event watch %q", ev.Watch)
		}
		if ev.Type == "item.price_changed" && (ev.Change.From != 100 || ev.Change.To != 90) {
			t.Errorf("price change %+v", ev.Change)
		}
	}
	for _, want := range []string{"item.new", "item.price_changed", "item.reserved"} {
		if !types[want] {
			t.Errorf("missing event %s in %v", want, types)
		}
	}
	// Second check: nothing changed, nothing reported, exit 0.
	r = h.must("", "watch", "check", "bici")
	if strings.TrimSpace(r.stdout) != "[]" {
		t.Fatalf("second check should be empty:\n%s", r.stdout)
	}
	ev := h.must("", "watch", "events", "bici")
	if strings.Count(ev.stdout, `"type"`) != 3 {
		t.Fatalf("events history:\n%s", ev.stdout)
	}
}

func TestWatchAddEmitInitialReportsBaseline(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.fake.AddItem(bike("hashnnnnnnnn", 10))
	h.fake.AddItem(bike("hashoooooooo", 20))
	r := h.must("", "watch", "add", "search", "bici", "--name", "seed", "--emit-initial", "--format", "jsonl")
	if strings.Count(r.stdout, `"item.new"`) != 2 {
		t.Fatalf("want 2 item.new events:\n%s", r.stdout)
	}
	if r := h.must("", "watch", "check", "seed"); strings.TrimSpace(r.stdout) != "[]" {
		t.Fatalf("baseline must be stored after emit-initial:\n%s", r.stdout)
	}
}

func TestItemWatchTracksReservedSoldAndRemoved(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(bike("hashpppppppp", 100))
	h.must("", "watch", "add", "item", it.Hash, "--name", "one")
	it.Reserved = true
	r := h.must("", "watch", "check", "one", "--format", "jsonl")
	if !strings.Contains(r.stdout, `"item.reserved"`) {
		t.Fatalf("reserved not reported:\n%s", r.stdout)
	}
	it.Sold = true
	r = h.must("", "watch", "check", "one", "--format", "jsonl")
	if !strings.Contains(r.stdout, `"item.sold"`) {
		t.Fatalf("sold not reported:\n%s", r.stdout)
	}
	it.Removed = true
	r = h.must("", "watch", "check", "one", "--format", "jsonl")
	if !strings.Contains(r.stdout, `"item.removed"`) {
		t.Fatalf("removal not reported:\n%s", r.stdout)
	}
	r = h.must("", "watch", "check", "one")
	if strings.TrimSpace(r.stdout) != "[]" {
		t.Fatalf("removal must be reported once:\n%s", r.stdout)
	}
}

func TestSellerWatchReportsNewAndGoneItems(t *testing.T) {
	h := newHarness(t)
	h.login()
	first := h.fake.AddItem(bike("hashqqqqqqqq", 10))
	h.must("", "watch", "add", "seller", "other-seller-68037934", "--name", "fran")
	h.fake.AddItem(bike("hashrrrrrrrr", 20))
	first.Removed = true
	r := h.must("", "watch", "check", "fran", "--format", "jsonl")
	if !strings.Contains(r.stdout, `"seller.new_item"`) || !strings.Contains(r.stdout, `"item.removed"`) {
		t.Fatalf("events:\n%s", r.stdout)
	}
}

func TestSinksReceiveEventsAndFailuresDoNotBlockCommit(t *testing.T) {
	h := newHarness(t)
	h.login()
	script := filepath.Join(h.home, "on-event.sh")
	seen := filepath.Join(h.home, "seen.jsonl")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >> "+seen+"\necho \"$WALLAPOP_EVENT_TYPE\" >> "+seen+".types\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.must("", "config", "set", "sinks.hook.type", "webhook")
	h.must("", "config", "set", "sinks.hook.url", h.fake.URL+"/hook")
	h.must("", "config", "set", "sinks.script.type", "exec")
	h.must("", "config", "set", "sinks.dead.type", "webhook")
	h.must("", "config", "set", "sinks.dead.url", h.fake.URL+"/nowhere")
	// command is a list; set it through the file since `config set` writes scalars.
	cfgPath := filepath.Join(h.home, "config", "wallapop-cli", "config.toml")
	raw, _ := os.ReadFile(cfgPath)
	raw = append(raw, []byte("\n[sinks.script]\ntype = \"exec\"\ncommand = [\""+script+"\"]\n")...)
	raw = []byte(strings.Replace(string(raw), "[sinks.script]\ntype = 'exec'\n", "", 1))
	if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	h.must("", "sink", "test", "script")

	h.fake.AddItem(bike("hashssssssss", 10))
	h.must("", "watch", "add", "search", "bici", "--name", "bici", "--notify", "hook", "--notify", "script", "--notify", "dead")
	h.fake.AddItem(bike("hashtttttttt", 20))
	r := h.must("", "watch", "check", "bici")
	if !strings.Contains(r.stderr, "sink dead") {
		t.Fatalf("dead sink failure should be reported on stderr: %q", r.stderr)
	}
	if len(h.fake.Hooks) != 1 || !strings.Contains(string(h.fake.Hooks[0]), `"item.new"`) {
		t.Fatalf("webhook payload: %v", h.fake.Hooks)
	}
	types, _ := os.ReadFile(seen + ".types")
	if !strings.Contains(string(types), "item.new") {
		t.Fatalf("exec sink did not run: %q", types)
	}
	// State was committed despite the dead sink: no re-emit.
	r = h.must("", "watch", "check", "bici", "--all")
	if strings.TrimSpace(r.stdout) != "[]" {
		t.Fatalf("event re-emitted after sink failure:\n%s", r.stdout)
	}
}

func TestWatchIntervalFloorAndUnknownSink(t *testing.T) {
	h := newHarness(t)
	h.login()
	r := h.run("", "watch", "add", "search", "x", "--name", "fast", "--interval", "5s")
	if r.code != 2 || !strings.Contains(r.stderr, "30s") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	r = h.run("", "watch", "add", "search", "x", "--name", "nosink", "--notify", "ghost")
	if r.code != 2 || !strings.Contains(r.stderr, "ghost") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	r = h.run("", "watch", "remove", "nothing", "--yes")
	if r.code != 4 {
		t.Fatalf("removing an unknown watch should be exit 4, got %d", r.code)
	}
}

// errors, output, http behaviour

func TestCloudFrontBlockFallsBackToBrowserUserAgentOnce(t *testing.T) {
	h := newHarness(t)
	h.fake.AddItem(bike("hashuuuuuuuu", 10))
	h.fake.BlockNextAPI = true
	r := h.must("", "search", "bici", "--lat", "40", "--lng", "-3")
	if !strings.Contains(r.stderr, "browser user agent") {
		t.Fatalf("expected a one-time notice, stderr %q", r.stderr)
	}
	reqs := h.fake.RequestsTo("/api/v3/search")
	if len(reqs) != 2 || !strings.HasPrefix(reqs[0].UA, "wallapop-cli/") || !strings.HasPrefix(reqs[1].UA, "Mozilla/") {
		t.Fatalf("requests: %+v", reqs)
	}
}

func TestAPIChangeProducesIssueLinkWithoutArgumentValues(t *testing.T) {
	h := newHarness(t)
	h.fake.AddItem(bike("hashvvvvvvvv", 10))
	h.fake.MalformedItem = true
	r := h.run("", "--error-format", "json", "item", "show", "hashvvvvvvvv")
	if r.code != 7 {
		t.Fatalf("exit %d stderr %s", r.code, r.stderr)
	}
	var env struct {
		Category string
		IssueURL string `json:"issue_url"`
	}
	decode(t, r.stderr, &env)
	if env.Category != "api_changed" || !strings.Contains(env.IssueURL, "template=api-change.yml") || !strings.Contains(env.IssueURL, "command=wallapop+item+show") || !strings.Contains(env.IssueURL, "version=test") {
		t.Fatalf("envelope %+v", env)
	}
	if strings.Contains(env.IssueURL, "endpoint=GET+%2Fapi%2Fv3%2Fitems%2Fhashvvvvvvvv") == false {
		t.Fatalf("endpoint path missing from %s", env.IssueURL)
	}
	text := h.run("", "item", "show", "hashvvvvvvvv")
	if !strings.Contains(text.stderr, "issues/new?") || !strings.Contains(text.stderr, "--debug") {
		t.Fatalf("text error should carry the link and the debug hint: %s", text.stderr)
	}
}

func TestPrettyOutputHonoursNoColorAndUsageErrorsExit2(t *testing.T) {
	h := newHarness(t)
	h.fake.AddItem(bike("hashwwwwwwww", 10))
	t.Setenv("NO_COLOR", "1")
	r := h.must("", "search", "bici", "--lat", "40", "--lng", "-3", "--format", "pretty")
	if strings.Contains(r.stdout, "\x1b[") || !strings.Contains(r.stdout, "HASH") {
		t.Fatalf("pretty output: %q", r.stdout)
	}
	r = h.run("", "search", "--format", "yaml")
	if r.code != 2 {
		t.Fatalf("unknown format should be exit 2, got %d", r.code)
	}
	r = h.run("", "search", "--sort", "cheapest", "--lat", "1", "--lng", "1")
	if r.code != 2 || !strings.Contains(r.stderr, "price_asc") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	r = h.run("", "item")
	if r.code != 0 || !strings.Contains(r.stdout, "Usage") {
		t.Fatalf("bare noun should print help: %d %q", r.code, r.stdout)
	}
	r = h.must("", "search", "bici", "--lat", "40", "--lng", "-3", "--format", "toon")
	if !strings.Contains(r.stdout, "items[1]") || !strings.Contains(r.stdout, "hash: hashwwwwwwww") {
		t.Fatalf("toon must use json names:\n%s", r.stdout)
	}
}

func TestConfigSetRejectsUnknownKeysAndTypesValues(t *testing.T) {
	h := newHarness(t)
	h.must("", "config", "set", "watch.interval", "10m")
	h.must("", "config", "set", "profiles.default.location.lat", "40.5")
	r := h.run("", "config", "set", "watch.intervall", "10m")
	if r.code != 2 {
		t.Fatalf("unknown key should be exit 2, got %d", r.code)
	}
	r = h.must("", "config", "get", "profiles.default.location.lat")
	if strings.TrimSpace(r.stdout) != "40.5" {
		t.Fatalf("got %q", r.stdout)
	}
	r = h.must("", "config", "list")
	if !strings.Contains(r.stdout, `"interval": "10m"`) {
		t.Fatalf("config list:\n%s", r.stdout)
	}
}

func TestSkillsAndDoctor(t *testing.T) {
	h := newHarness(t)
	r := h.must("", "skills", "get", "wallapop-usage")
	if !strings.Contains(r.stdout, "Exit codes") {
		t.Fatal("skill doc missing")
	}
	r = h.run("", "doctor")
	if r.code != 2 || !strings.Contains(r.stdout, `"name": "session"`) {
		t.Fatalf("doctor without session should fail: %d\n%s", r.code, r.stdout)
	}
	h.login()
	r = h.must("", "doctor")
	if !strings.Contains(r.stdout, `"ok": true`) || !strings.Contains(r.stdout, "pubnub token issued") {
		t.Fatalf("doctor:\n%s", r.stdout)
	}
}

func TestSessionTokenEnvOverridesCredentials(t *testing.T) {
	h := newHarness(t)
	t.Setenv("WALLAPOP_SESSION_TOKEN", fakewallapop.ValidCookie)
	r := h.must("", "me", "show")
	if !strings.Contains(r.stdout, fakewallapop.UserHash) {
		t.Fatalf("me show:\n%s", r.stdout)
	}
	if _, err := os.Stat(h.credentialsPath()); !os.IsNotExist(err) {
		t.Fatal("env session must never be written to disk")
	}
}

// session

func TestWatchCheckMintsOnceToKeepSessionAliveOnlyWhenLoggedIn(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.must("", "watch", "add", "search", "bike", "--name", "bikes")
	mints := len(h.fake.RequestsTo("/api/auth/session"))

	// Search watches are public, so the only reason to mint is the keepalive.
	h.must("", "watch", "check", "--all")
	if got := len(h.fake.RequestsTo("/api/auth/session")) - mints; got != 1 {
		t.Fatalf("watch check minted %d times, want exactly 1", got)
	}
	raw, _ := os.ReadFile(h.credentialsPath())
	if !strings.Contains(string(raw), "session_expires") {
		t.Fatalf("keepalive must persist the rotated cookie's expiry:\n%s", raw)
	}

	// A rejected session is a warning: the watches still ran, exit stays 0.
	h.fake.MintEmpty = true
	r := h.must("", "watch", "check", "--all")
	if !strings.Contains(r.stderr, "keepalive") || !strings.Contains(r.stderr, "auth login") {
		t.Fatalf("expected a keepalive warning on stderr, got: %q", r.stderr)
	}

	h.fake.MintEmpty = false
	h.must("", "auth", "logout")
	mints = len(h.fake.RequestsTo("/api/auth/session"))
	h.must("", "watch", "check", "--all")
	if got := len(h.fake.RequestsTo("/api/auth/session")) - mints; got != 0 {
		t.Fatalf("watch check without a session minted %d times, want 0", got)
	}
}

func TestAuthRefreshReportsNewExpiryAndAuthStatusShowsIt(t *testing.T) {
	h := newHarness(t)
	h.login()
	mints := len(h.fake.RequestsTo("/api/auth/session"))
	// Age the stored expiry so a refresh that failed to persist would show.
	raw, _ := os.ReadFile(h.credentialsPath())
	stale := regexp.MustCompile(`session_expires = .*`).ReplaceAll(raw, []byte("session_expires = 2020-01-01T00:00:00Z"))
	if err := os.WriteFile(h.credentialsPath(), stale, 0o600); err != nil {
		t.Fatal(err)
	}

	r := h.must("", "auth", "refresh")
	var out struct {
		Profile        string     `json:"profile"`
		Account        string     `json:"account"`
		SessionExpires *time.Time `json:"session_expires"`
	}
	decode(t, r.stdout, &out)
	if out.Profile != "default" || out.Account == "" || out.SessionExpires == nil {
		t.Fatalf("auth refresh output: %s", r.stdout)
	}
	if time.Until(*out.SessionExpires) < 29*24*time.Hour {
		t.Fatalf("expiry should come from the rotated cookie (~30 days), got %s", out.SessionExpires)
	}
	if got := len(h.fake.RequestsTo("/api/auth/session")) - mints; got != 1 {
		t.Fatalf("auth refresh minted %d times, want 1", got)
	}
	r = h.must("", "auth", "status")
	var st struct {
		SessionExpires *time.Time `json:"session_expires"`
	}
	decode(t, r.stdout, &st)
	if st.SessionExpires == nil || !st.SessionExpires.Equal(*out.SessionExpires) {
		t.Fatalf("auth status should show the stored expiry %s, got %s", out.SessionExpires, r.stdout)
	}

	h.fake.MintEmpty = true
	if r := h.run("", "auth", "refresh"); r.code != 3 {
		t.Fatalf("rejected session should exit 3, got %d: %s", r.code, r.stderr)
	}
}

// schedule

func TestWatchCheckRotatesTheServiceLogAtTheCap(t *testing.T) {
	h := newHarness(t)
	logDir := filepath.Join(h.home, "state", "wallapop-cli")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(logDir, "wallapop-watch-default.log")
	if err := os.WriteFile(logFile, bytes.Repeat([]byte("x"), 1<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	h.must("", "watch", "check", "--all")
	if _, err := os.Stat(logFile + ".1"); err != nil {
		t.Fatalf("oversized log should rotate to .1: %v", err)
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Fatal("the scheduler should get a fresh log on its next run")
	}
	// A second rotation overwrites .1 rather than growing a chain.
	if err := os.WriteFile(logFile, bytes.Repeat([]byte("y"), 1<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	h.must("", "watch", "check", "--all")
	if raw, _ := os.ReadFile(logFile + ".1"); len(raw) == 0 || raw[0] != 'y' {
		t.Fatal("second rotation should replace .1")
	}
	if entries, _ := os.ReadDir(logDir); len(entries) != 1 {
		t.Fatalf("expected only the rotated log, got %d files", len(entries))
	}
}

func TestServiceStatusAndDoctorReportNotInstalledWithoutUnitFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no scheduler integration on windows")
	}
	h := newHarness(t)
	r := h.must("", "watch", "service", "status")
	var v struct {
		State string `json:"state"`
	}
	decode(t, r.stdout, &v)
	if v.State != "not installed" {
		t.Fatalf("status without unit files: %q", v.State)
	}
	h.login()
	r = h.must("", "doctor")
	if !strings.Contains(r.stdout, `"name": "schedule"`) || !strings.Contains(r.stdout, `"detail": "not installed"`) {
		t.Fatalf("doctor should report the schedule state:\n%s", r.stdout)
	}
}

// alerts (saved searches)

func TestAlertListAndWatchFromAlertReuseTheSavedQuery(t *testing.T) {
	h := newHarness(t)
	h.login()
	r := h.must("", "alert", "list")
	var alerts []struct {
		ID       string                `json:"id"`
		Keywords string                `json:"keywords"`
		Label    string                `json:"location_label"`
		Location struct{ Lat float64 } `json:"location"`
		RadiusKm int                   `json:"radius_km"`
		Filters  map[string]string     `json:"filters"`
		Enabled  bool                  `json:"enabled"`
	}
	decode(t, r.stdout, &alerts)
	if len(alerts) != 1 || alerts[0].ID != fakewallapop.SavedSearchID || alerts[0].Keywords != "thinkpad x1" || alerts[0].Label == "" || alerts[0].Location.Lat != 41.39 || alerts[0].RadiusKm != 50 || !alerts[0].Enabled {
		t.Fatalf("alert list: %s", r.stdout)
	}
	if alerts[0].Filters["max_sale_price"] != "400" || alerts[0].Filters["category_id"] != "24200" || alerts[0].Filters["country_code"] != "" {
		t.Fatalf("filters should carry the query minus the location and bookkeeping keys: %v", alerts[0].Filters)
	}

	before := len(h.fake.RequestsTo("/api/v3/search"))
	h.must("", "watch", "add", "search", "--from-alert", fakewallapop.SavedSearchID, "--name", "tp")
	reqs := h.fake.RequestsTo("/api/v3/search")
	if len(reqs) != before+1 {
		t.Fatalf("the baseline check should run one search, got %d", len(reqs)-before)
	}
	q := reqs[len(reqs)-1].Query
	want := map[string]string{"keywords": "thinkpad x1", "latitude": "41.39", "longitude": "2.17", "distance_in_km": "50", "max_sale_price": "400", "order_by": "newest", "category_id": "24200", "condition": "good,fair"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("query %s = %q, want %q", k, q.Get(k), v)
		}
	}
	for _, k := range []string{"country_code", "saved_search_id", "category_ids"} {
		if q.Has(k) {
			t.Errorf("query must not forward %s", k)
		}
	}

	if r := h.run("", "watch", "add", "search", "--from-alert", "00000000-0000-0000-0000-000000000000", "--name", "nope"); r.code != 4 {
		t.Fatalf("unknown alert should exit 4, got %d: %s", r.code, r.stderr)
	}
	if r := h.run("", "watch", "add", "search", "bici", "--from-alert", fakewallapop.SavedSearchID, "--name", "mixed"); r.code != 2 {
		t.Fatalf("keywords plus --from-alert should be a usage error, got %d", r.code)
	}
	if r := h.run("", "watch", "add", "search", "--from-alert=", "--name", "empty"); r.code != 2 {
		t.Fatalf("an empty --from-alert must not fall through to a keyword-less watch, got %d", r.code)
	}
}

func TestSellerActionsOnAnotherSellersItemFailBeforeWriting(t *testing.T) {
	h := newHarness(t)
	h.login()
	theirs := h.fake.AddItem(fakewallapop.Item{Hash: "hashtheirs00", Title: "Theirs", Price: 5, Seller: fakewallapop.OtherHash})
	for _, args := range [][]string{{"item", "sold", theirs.Hash, "--yes"}, {"item", "delete", theirs.Hash, "--yes"}, {"item", "reserve", theirs.Hash}} {
		r := h.run("", args...)
		if r.code != 2 || !strings.Contains(r.stderr, "another seller") {
			t.Fatalf("%v: exit %d stderr %q", args, r.code, r.stderr)
		}
	}
	for _, rq := range h.fake.RequestsUnder("/api/v3/items/" + theirs.Hash) {
		if rq.Method != "GET" {
			t.Fatalf("no write may reach wallapop for someone else's item, saw %s %s", rq.Method, rq.Path)
		}
	}
}

func testPNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestItemCreatePublishesWithImages(t *testing.T) {
	h := newHarness(t)
	h.login()
	img1 := filepath.Join(h.home, "a.png")
	img2 := filepath.Join(h.home, "b.png")
	testPNG(t, img1)
	testPNG(t, img2)
	r := h.must("", "item", "create",
		"--title", "Test Bike", "--description", "A test bike", "--price", "120",
		"--category", "17001", "--condition", "good",
		"--image", img1, "--image", img2)
	var created struct {
		Hash string
		URL  string
	}
	decode(t, r.stdout, &created)
	if len(created.Hash) != 12 || created.URL == "" {
		t.Fatalf("bad created item: %s", r.stdout)
	}
	if got := h.fake.CreateCalls; got != 1 {
		t.Fatalf("CreateCalls = %d, want 1", got)
	}
	total := 0
	for _, n := range h.fake.Uploads {
		total += n
	}
	if total != 2 {
		t.Fatalf("uploaded pictures = %d, want 2", total)
	}
	listed := h.must("", "me", "items")
	if !strings.Contains(listed.stdout, created.Hash) {
		t.Fatalf("created item missing from me items:\n%s", listed.stdout)
	}
	shown := h.must("", "item", "show", created.Hash)
	var detail struct {
		Title     string
		Price     float64
		Condition string
	}
	decode(t, shown.stdout, &detail)
	if detail.Title != "Test Bike" || detail.Price != 120 || detail.Condition != "good" {
		t.Fatalf("wrong detail: %s", shown.stdout)
	}
}

func TestItemCreateMissingFieldsIsUsageError(t *testing.T) {
	h := newHarness(t)
	h.login()
	r := h.run("", "item", "create", "--title", "Half")
	if r.code != 2 {
		t.Fatalf("exit %d, want 2: %s", r.code, r.stderr)
	}
	for _, want := range []string{"--description", "--price", "--category", "--condition", "--image"} {
		if !strings.Contains(r.stderr, want) {
			t.Fatalf("stderr missing %s: %s", want, r.stderr)
		}
	}
	if h.fake.CreateCalls != 0 {
		t.Fatal("create reached Wallapop despite missing fields")
	}
}

func TestItemCreateRejectsUnknownAttr(t *testing.T) {
	h := newHarness(t)
	h.login()
	img := filepath.Join(h.home, "a.png")
	testPNG(t, img)
	r := h.run("", "item", "create",
		"--title", "T", "--description", "D", "--price", "5",
		"--category", "17001", "--condition", "good",
		"--attr", "wingspan=2", "--image", img)
	if r.code != 2 || !strings.Contains(r.stderr, "wingspan") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	if h.fake.CreateCalls != 0 {
		t.Fatal("create reached Wallapop despite bad attr")
	}
}

// The common fields have typed flags; --attr must not be a second way in,
// or `--attr title=` would silently beat `--title`.
// Half a coordinate pair would publish the listing at a real latitude and a
// zero longitude, so it is refused before anything is sent.
func TestItemCreateRejectsHalfACoordinatePair(t *testing.T) {
	h := newHarness(t)
	h.login()
	img := filepath.Join(h.home, "a.png")
	testPNG(t, img)
	r := h.run("", "item", "create",
		"--title", "T", "--description", "D", "--price", "5",
		"--category", "17001", "--condition", "good",
		"--lat", "41.39", "--image", img)
	if r.code != 2 || !strings.Contains(r.stderr, "--lng") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	if h.fake.CreateCalls != 0 {
		t.Fatal("create reached Wallapop with half a coordinate pair")
	}
}

// An image whose name carries a quote must still arrive as a readable
// multipart part.
func TestItemCreateAcceptsQuotedImageName(t *testing.T) {
	h := newHarness(t)
	h.login()
	img := filepath.Join(h.home, `a"b.png`)
	testPNG(t, img)
	h.must("", "item", "create",
		"--title", "T", "--description", "D", "--price", "5",
		"--category", "17001", "--condition", "good", "--image", img)
	if h.fake.CreateCalls != 1 {
		t.Fatalf("create calls = %d, want 1", h.fake.CreateCalls)
	}
}

func TestItemCreateRejectsCommonAttr(t *testing.T) {
	h := newHarness(t)
	h.login()
	img := filepath.Join(h.home, "a.png")
	testPNG(t, img)
	r := h.run("", "item", "create",
		"--title", "T", "--description", "D", "--price", "5",
		"--category", "17001", "--condition", "good",
		"--attr", "Price_Amount=9", "--image", img)
	if r.code != 2 || !strings.Contains(r.stderr, "--price") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	if h.fake.CreateCalls != 0 {
		t.Fatal("create reached Wallapop despite a common --attr key")
	}
}

func TestItemEditChangesFields(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(fakewallapop.Item{Hash: "hasheeeddddd", Title: "Old", Description: "Old desc", Price: 10, Seller: fakewallapop.UserHash})
	img := filepath.Join(h.home, "new.png")
	testPNG(t, img)
	r := h.must("", "item", "edit", it.Hash, "--title", "New", "--description", "New desc", "--price", "50", "--condition", "new", "--image", img)
	var edited struct {
		Title       string
		Description string
		Price       float64
		Condition   string
		Images      []string
	}
	decode(t, r.stdout, &edited)
	if edited.Title != "New" || edited.Description != "New desc" || edited.Price != 50 || edited.Condition != "new" {
		t.Fatalf("wrong edit result: %s", r.stdout)
	}
	if len(edited.Images) != 1 {
		t.Fatalf("edited images = %d, want 1: %s", len(edited.Images), r.stdout)
	}
	shown := h.must("", "item", "show", it.Hash)
	var detail struct {
		Description string
		Images      []string
	}
	decode(t, shown.stdout, &detail)
	if detail.Description != "New desc" {
		t.Fatalf("description not served back: %s", shown.stdout)
	}
	if len(detail.Images) != 1 {
		t.Fatalf("shown images = %d, want 1: %s", len(detail.Images), shown.stdout)
	}
}

// An edit resends the whole attribute set, so a title-only change must not
// drop the listing's category attributes.
func TestItemEditKeepsCategoryAttrs(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(fakewallapop.Item{
		Hash: "hasheeeddddw", Title: "Old", Price: 10, Seller: fakewallapop.UserHash,
		Attrs: map[string]string{"brand": "Volkswagen"},
	})
	h.must("", "item", "edit", it.Hash, "--title", "New")
	if got := h.fake.Items[it.Hash].Attrs["brand"]; got != "Volkswagen" {
		t.Fatalf("brand after title edit = %q, want Volkswagen", got)
	}
}

// An edit resends the fields it is not changing, so those must come from the
// detail endpoint: a stale rendered page would revert the listing's title.
func TestItemEditDoesNotRevertToAStalePageTitle(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(fakewallapop.Item{
		Hash: "hasheeeddddq", Title: "Fresh", PageTitle: "Stale",
		Price: 10, Seller: fakewallapop.UserHash,
	})
	h.must("", "item", "edit", it.Hash, "--price", "50")
	if got := h.fake.Items[it.Hash].Title; got != "Fresh" {
		t.Fatalf("title after a price-only edit = %q, want Fresh", got)
	}
}

func TestItemEditForeignIsRefused(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(bike("hasheeeeeeff", 20))
	r := h.run("", "item", "edit", it.Hash, "--title", "Mine now")
	if r.code != 2 {
		t.Fatalf("exit %d, want 2: %s", r.code, r.stderr)
	}
	for _, rq := range h.fake.RequestsUnder("/api/v3/items/" + it.Hash) {
		if rq.Method == "PUT" {
			t.Fatal("PUT sent for another seller's item")
		}
	}
}

func TestItemEditNoChangesIsUsageError(t *testing.T) {
	h := newHarness(t)
	h.login()
	it := h.fake.AddItem(fakewallapop.Item{Hash: "hasheeeddaaa", Title: "Mine", Price: 5, Seller: fakewallapop.UserHash})
	r := h.run("", "item", "edit", it.Hash)
	if r.code != 2 {
		t.Fatalf("exit %d, want 2: %s", r.code, r.stderr)
	}
}
