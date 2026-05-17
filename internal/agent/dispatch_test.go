package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClaude struct {
	reply string
	err   error
	lastS string
	lastU string
}

func (f *fakeClaude) Reply(ctx context.Context, system, user string) (string, error) {
	f.lastS = system
	f.lastU = user
	return f.reply, f.err
}

type fakeExec struct {
	mu sync.Mutex

	sendCalls           []sendCall
	broadcastCalls      []broadcastCall
	searchCalls         []searchCall
	pendingCalls        int
	summaryCalls        []int
	getProfileCalls     []ProfileQuery
	updateProfileCalls  []updateProfileCall
	extractProfileCalls []string
	listContactsCalls   []listContactsCall
	resolveCalls        []string
	resolveResult       []ContactSummary

	sendErr      error
	broadcastErr error
	searchErr    error
	pendingErr   error
	summaryErr   error
	profileErr   error
	contactsErr  error

	broadcastID string
	searchHits  []KBHit
	pendings    []PendingSummary
	inboxBucket []InboxSummary
	profile       *ProfileInfo
	contacts      []ContactSummary
	contactsTotal int
}

type sendCall struct{ Phone, Message, FromJID string }
type broadcastCall struct {
	Recipients []string
	Message    string
}
type searchCall struct {
	Query string
	Limit int
}

func (f *fakeExec) SendWhatsAppMessage(ctx context.Context, phone, msg, fromJID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalls = append(f.sendCalls, sendCall{phone, msg, fromJID})
	return f.sendErr
}

func (f *fakeExec) BroadcastWhatsApp(ctx context.Context, rs []string, msg string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.broadcastCalls = append(f.broadcastCalls, broadcastCall{rs, msg})
	return f.broadcastID, f.broadcastErr
}

func (f *fakeExec) SearchKB(ctx context.Context, q string, lim int) ([]KBHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searchCalls = append(f.searchCalls, searchCall{q, lim})
	return f.searchHits, f.searchErr
}

func (f *fakeExec) ListPendingReplies(ctx context.Context) ([]PendingSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pendingCalls++
	return f.pendings, f.pendingErr
}

func (f *fakeExec) SummarizeInbox(ctx context.Context, h int) ([]InboxSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaryCalls = append(f.summaryCalls, h)
	return f.inboxBucket, f.summaryErr
}

func (f *fakeExec) ResolveContact(ctx context.Context, query string) ([]ContactSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolveCalls = append(f.resolveCalls, query)
	return f.resolveResult, f.contactsErr
}

func (f *fakeExec) ListContacts(ctx context.Context, search string, limit int) ([]ContactSummary, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listContactsCalls = append(f.listContactsCalls, listContactsCall{search, limit})
	total := f.contactsTotal
	if total == 0 {
		total = len(f.contacts)
	}
	return f.contacts, total, f.contactsErr
}

type listContactsCall struct {
	Search string
	Limit  int
}

func (f *fakeExec) GetProfile(ctx context.Context, q ProfileQuery) (*ProfileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getProfileCalls = append(f.getProfileCalls, q)
	return f.profile, f.profileErr
}

func (f *fakeExec) UpdateProfile(ctx context.Context, jid, field, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateProfileCalls = append(f.updateProfileCalls, updateProfileCall{jid, field, value})
	return f.profileErr
}

func (f *fakeExec) ExtractProfile(ctx context.Context, jid string) (*ProfileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extractProfileCalls = append(f.extractProfileCalls, jid)
	return f.profile, f.profileErr
}

type updateProfileCall struct{ JID, Field, Value string }

type fakeStore struct {
	mu     sync.Mutex
	got    []string
	recent []DispatchTurn
}

func (f *fakeStore) SaveDispatchLog(ctx context.Context, ch, oid, msg, act, reply, errText string, dur int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, act+":"+reply)
	return nil
}

func (f *fakeStore) RecentDispatchTurns(ctx context.Context, channel, ownerID string, since time.Duration, limit int) ([]DispatchTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recent, nil
}

func newTestDispatcher(reply string) (*Dispatcher, *fakeClaude, *fakeExec, *fakeStore) {
	c := &fakeClaude{reply: reply}
	ex := &fakeExec{}
	st := &fakeStore{}
	return NewDispatcher(c, ex, st), c, ex, st
}

func TestRun_ReplyActionPassesThrough(t *testing.T) {
	d, _, ex, st := newTestDispatcher(`{"action":"reply","params":{},"user_reply":"hello there"}`)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "hi"})
	if res.Action != ActionReply {
		t.Errorf("action=%s", res.Action)
	}
	if res.UserReply != "hello there" {
		t.Errorf("reply=%q", res.UserReply)
	}
	if len(ex.sendCalls)+len(ex.broadcastCalls) != 0 {
		t.Errorf("reply must not invoke executor")
	}
	// audit log written
	waitForLog(t, st, 1)
}

func TestRun_SendWhatsAppDispatches(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"send_whatsapp","params":{"phone":"60111","message":"hi"},"user_reply":"On it."}`)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram"})
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if len(ex.sendCalls) != 1 || ex.sendCalls[0].Phone != "60111" || ex.sendCalls[0].Message != "hi" {
		t.Errorf("send calls: %+v", ex.sendCalls)
	}
	if !strings.Contains(res.UserReply, "60111") {
		t.Errorf("reply missing status: %s", res.UserReply)
	}
}

func TestRun_BroadcastDispatches(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"broadcast_whatsapp","params":{"recipients":["60111","60222"],"message":"sale"},"user_reply":"Broadcasting."}`)
	ex.broadcastID = "batch_abc"
	res := d.Run(context.Background(), DispatchInput{})
	if res.Error != "" {
		t.Fatalf("err: %s", res.Error)
	}
	if len(ex.broadcastCalls) != 1 || len(ex.broadcastCalls[0].Recipients) != 2 {
		t.Errorf("broadcast: %+v", ex.broadcastCalls)
	}
	if !strings.Contains(res.UserReply, "batch_abc") {
		t.Errorf("missing batch id: %s", res.UserReply)
	}
}

func TestRun_SearchKBReturnsHits(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"search_kb","params":{"query":"policy"},"user_reply":"Found:"}`)
	ex.searchHits = []KBHit{{Filename: "policy.pdf", Summary: "renewal terms"}}
	res := d.Run(context.Background(), DispatchInput{})
	if !strings.Contains(res.UserReply, "policy.pdf") {
		t.Errorf("reply missing hit: %s", res.UserReply)
	}
	if ex.searchCalls[0].Limit != 5 {
		t.Errorf("default limit not applied: %d", ex.searchCalls[0].Limit)
	}
}

func TestRun_SearchKBEmptyResults(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"search_kb","params":{"query":"x"},"user_reply":"Looking."}`)
	res := d.Run(context.Background(), DispatchInput{})
	if !strings.Contains(res.UserReply, "no matches") {
		t.Errorf("expected 'no matches' suffix: %s", res.UserReply)
	}
}

func TestRun_SummaryInboxDefaultsTo24h(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"summary_inbox","params":{},"user_reply":"Inbox:"}`)
	ex.inboxBucket = []InboxSummary{{Sender: "Alice", JID: "601@s", Count: 3, LastBody: "thanks"}}
	d.Run(context.Background(), DispatchInput{})
	if len(ex.summaryCalls) != 1 || ex.summaryCalls[0] != 24 {
		t.Errorf("hours default not applied: %v", ex.summaryCalls)
	}
}

func TestRun_ListPendingDispatches(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"list_pending","params":{},"user_reply":"Pending:"}`)
	ex.pendings = []PendingSummary{{ID: 1, ContactJID: "601@s", Incoming: "hi"}}
	res := d.Run(context.Background(), DispatchInput{})
	if ex.pendingCalls != 1 {
		t.Errorf("ListPendingReplies not called")
	}
	if !strings.Contains(res.UserReply, "#1") {
		t.Errorf("pending id missing: %s", res.UserReply)
	}
}

func TestRun_MalformedJSONFallsBackToReply(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`oh hi I am claude. action is reply.`)
	res := d.Run(context.Background(), DispatchInput{})
	if res.Action != ActionReply {
		t.Errorf("action=%s", res.Action)
	}
	if !strings.Contains(res.UserReply, "claude") {
		t.Errorf("fallback should preserve raw text: %s", res.UserReply)
	}
	if len(ex.sendCalls) != 0 {
		t.Errorf("malformed must not invoke executor")
	}
}

func TestRun_JSONWrappedInFenceWorks(t *testing.T) {
	raw := "```json\n{\"action\":\"reply\",\"params\":{},\"user_reply\":\"ack\"}\n```"
	d, _, _, _ := newTestDispatcher(raw)
	res := d.Run(context.Background(), DispatchInput{})
	if res.Action != ActionReply || res.UserReply != "ack" {
		t.Errorf("got action=%s reply=%q", res.Action, res.UserReply)
	}
}

func TestRun_SendValidationRequiresPhoneAndMessage(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"send_whatsapp","params":{"phone":""},"user_reply":"sending"}`)
	res := d.Run(context.Background(), DispatchInput{})
	if res.Error == "" {
		t.Errorf("expected validation error")
	}
	if !strings.Contains(res.UserReply, "failed") {
		t.Errorf("user reply should include failure: %s", res.UserReply)
	}
}

func TestRun_ClaudeErrorReturnedAsReply(t *testing.T) {
	d, c, _, _ := newTestDispatcher("")
	c.err = errors.New("rate limited")
	res := d.Run(context.Background(), DispatchInput{Message: "hi"})
	if res.Error == "" {
		t.Errorf("expected error on Claude failure")
	}
	if !strings.Contains(res.UserReply, "rate limited") {
		t.Errorf("user reply should reflect claude err: %s", res.UserReply)
	}
}

func TestRun_UnknownActionReturnsError(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"delete_database","params":{},"user_reply":"sure"}`)
	res := d.Run(context.Background(), DispatchInput{})
	if res.Error == "" {
		t.Errorf("expected error for unknown action")
	}
}

func TestRun_NoClaudeReturnsOfflineMessage(t *testing.T) {
	d := NewDispatcher(nil, &fakeExec{}, &fakeStore{})
	res := d.Run(context.Background(), DispatchInput{Message: "hi"})
	if res.Error == "" {
		t.Errorf("expected error")
	}
	if !strings.Contains(res.UserReply, "offline") {
		t.Errorf("expected 'offline': %s", res.UserReply)
	}
}

func TestParseDispatch_HandlesPrefixedText(t *testing.T) {
	p, err := parseDispatch(`Here is the response:\n{"action":"reply","params":{},"user_reply":"hi"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.UserReply != "hi" {
		t.Errorf("got %q", p.UserReply)
	}
}

func TestRun_GetProfileByName(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"get_profile","params":{"name":"Alice"},"user_reply":"Profile:"}`)
	ex.profile = &ProfileInfo{JID: "601@s", DisplayName: "Alice", Role: "client", Interests: []string{"insurance"}}
	res := d.Run(context.Background(), DispatchInput{})
	if len(ex.getProfileCalls) != 1 || ex.getProfileCalls[0].Name != "Alice" {
		t.Errorf("getProfile not called as expected: %+v", ex.getProfileCalls)
	}
	if !strings.Contains(res.UserReply, "Alice") || !strings.Contains(res.UserReply, "insurance") {
		t.Errorf("profile not rendered: %s", res.UserReply)
	}
}

func TestRun_GetProfileNoMatch(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"get_profile","params":{"jid":"601@s"},"user_reply":"Lookup:"}`)
	ex.profile = nil
	res := d.Run(context.Background(), DispatchInput{})
	if !strings.Contains(res.UserReply, "no profile") {
		t.Errorf("expected 'no profile': %s", res.UserReply)
	}
}

func TestRun_GetProfileRequiresJIDOrName(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"get_profile","params":{},"user_reply":"Looking."}`)
	res := d.Run(context.Background(), DispatchInput{})
	if res.Error == "" {
		t.Errorf("expected validation error")
	}
}

func TestRun_UpdateProfileDispatches(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"update_profile","params":{"jid":"601@s","field":"custom_notes","value":"VIP"},"user_reply":"Saving."}`)
	res := d.Run(context.Background(), DispatchInput{})
	if res.Error != "" {
		t.Fatalf("err: %s", res.Error)
	}
	if len(ex.updateProfileCalls) != 1 ||
		ex.updateProfileCalls[0].JID != "601@s" ||
		ex.updateProfileCalls[0].Field != "custom_notes" ||
		ex.updateProfileCalls[0].Value != "VIP" {
		t.Errorf("update profile call wrong: %+v", ex.updateProfileCalls)
	}
}

func TestRun_UpdateProfileRequiresJIDAndField(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"update_profile","params":{"jid":"601@s"},"user_reply":"Saving."}`)
	res := d.Run(context.Background(), DispatchInput{})
	if res.Error == "" {
		t.Errorf("expected validation error")
	}
}

func TestRun_ExtractProfileDispatches(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"extract_profile","params":{"jid":"601@s"},"user_reply":"Extracting."}`)
	ex.profile = &ProfileInfo{JID: "601@s", DisplayName: "Alice"}
	res := d.Run(context.Background(), DispatchInput{})
	if len(ex.extractProfileCalls) != 1 || ex.extractProfileCalls[0] != "601@s" {
		t.Errorf("extract not invoked: %+v", ex.extractProfileCalls)
	}
	if !strings.Contains(res.UserReply, "Alice") {
		t.Errorf("profile not in reply: %s", res.UserReply)
	}
}

// waitForLog gives the async logger up to 1s to record N entries.
func waitForLog(t *testing.T, st *fakeStore, want int) {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		n := len(st.got)
		st.mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.got) < want {
		t.Errorf("log: got %d, want %d", len(st.got), want)
	}
}
