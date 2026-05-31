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
	reply   string   // single-reply mode (back-compat)
	replies []string // sequence mode; one per Reply call, repeats last past the end
	calls   int
	err     error
	lastS   string
	lastU   string
}

func (f *fakeClaude) Reply(ctx context.Context, system, user string) (string, error) {
	f.lastS = system
	f.lastU = user
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if len(f.replies) > 0 {
		i := f.calls - 1
		if i >= len(f.replies) {
			i = len(f.replies) - 1
		}
		return f.replies[i], nil
	}
	return f.reply, nil
}

// newTestDispatcherSeq builds a dispatcher whose fake Claude returns the given
// replies in order (repeating the last past the end). Use for multi-step tests.
func newTestDispatcherSeq(replies ...string) (*Dispatcher, *fakeClaude, *fakeExec, *fakeStore) {
	c := &fakeClaude{replies: replies}
	ex := &fakeExec{}
	st := &fakeStore{}
	return NewDispatcher(c, ex, st), c, ex, st
}

type fakeExec struct {
	mu sync.Mutex

	sendCalls           []sendCall
	sendImageCalls      []sendImageCall
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
	coworkListCalls     []string
	coworkReadCalls     []string
	coworkSearchCalls   []coworkSearchCall
	coworkEditCalls     []coworkEditCall
	coworkCreateCalls   []coworkCreateCall
	coworkPathCalls     []string

	sendErr      error
	broadcastErr error
	searchErr    error
	pendingErr   error
	summaryErr   error
	profileErr   error
	contactsErr  error

	broadcastID   string
	searchHits    []KBHit
	pendings      []PendingSummary
	inboxBucket   []InboxSummary
	profile       *ProfileInfo
	contacts      []ContactSummary
	contactsTotal int

	coworkListResult   []CoworkFile
	coworkReadResult   *CoworkRead
	coworkSearchResult []CoworkHit
	coworkEditResult   *CoworkFile
	coworkCreateResult *CoworkFile
	coworkPathResult   string
	coworkErr          error

	kbEntries []KBEntry
	kbContent string
	note      *NoteView
	backlinks []string
	noteHits  []NoteHit

	ownerProfile     string // returned by GetOwnerProfile
	ownerProfileErr  error  // returned by GetOwnerProfile when set
	updatedProfile   string // captured by UpdateOwnerProfile
	updatedProfileMd string // captured mode
}

type coworkSearchCall struct {
	Query string
	Days  int
}
type coworkCreateCall struct {
	Date     string
	Filename string
	Content  string
}
type coworkEditCall struct {
	Filename string
	Op       string
	Content  string
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

type sendImageCall struct {
	Phone, Image, Caption, FromJID string
}

func (f *fakeExec) SendWhatsAppImage(ctx context.Context, phone, image, caption, fromJID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendImageCalls = append(f.sendImageCalls, sendImageCall{phone, image, caption, fromJID})
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

func (f *fakeExec) ListCowork(ctx context.Context, date string) ([]CoworkFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.coworkListCalls = append(f.coworkListCalls, date)
	return f.coworkListResult, f.coworkErr
}

func (f *fakeExec) ReadCowork(ctx context.Context, filename string) (*CoworkRead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.coworkReadCalls = append(f.coworkReadCalls, filename)
	return f.coworkReadResult, f.coworkErr
}

func (f *fakeExec) SearchCowork(ctx context.Context, query string, days int) ([]CoworkHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.coworkSearchCalls = append(f.coworkSearchCalls, coworkSearchCall{query, days})
	return f.coworkSearchResult, f.coworkErr
}

func (f *fakeExec) EditCowork(ctx context.Context, filename, op, content string) (*CoworkFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.coworkEditCalls = append(f.coworkEditCalls, coworkEditCall{filename, op, content})
	return f.coworkEditResult, f.coworkErr
}

func (f *fakeExec) CreateCowork(ctx context.Context, date, filename, content string) (*CoworkFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.coworkCreateCalls = append(f.coworkCreateCalls, coworkCreateCall{date, filename, content})
	return f.coworkCreateResult, f.coworkErr
}

func (f *fakeExec) CoworkPath(ctx context.Context, date string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.coworkPathCalls = append(f.coworkPathCalls, date)
	return f.coworkPathResult, f.coworkErr
}

func (f *fakeExec) ListKB(ctx context.Context, subdir string) ([]KBEntry, error) {
	return f.kbEntries, nil
}
func (f *fakeExec) ReadKB(ctx context.Context, path string) (string, *KBEntry, error) {
	return f.kbContent, &KBEntry{Name: path, IsText: true}, nil
}
func (f *fakeExec) ReadNote(ctx context.Context, name string) (*NoteView, error) {
	return f.note, nil
}
func (f *fakeExec) Backlinks(ctx context.Context, name string) ([]string, error) {
	return f.backlinks, nil
}
func (f *fakeExec) SearchNotes(ctx context.Context, query, tag string) ([]NoteHit, error) {
	return f.noteHits, nil
}

func (f *fakeExec) GetOwnerProfile(ctx context.Context) (string, error) {
	return f.ownerProfile, f.ownerProfileErr
}

func (f *fakeExec) UpdateOwnerProfile(ctx context.Context, content, mode string) error {
	f.updatedProfile = content
	f.updatedProfileMd = mode
	return nil
}

type updateProfileCall struct{ JID, Field, Value string }

type fakeStore struct {
	mu  sync.Mutex
	got []string

	session         DispatchSession
	tail            []DispatchTurn
	recallHits      []SessionSummaryHit
	resolvedChannel string
	resolvedOwner   string
	loggedSessionID int64
}

func (f *fakeStore) SaveDispatchLog(ctx context.Context, sessionID int64, ch, oid, msg, act, reply, errText string, dur int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loggedSessionID = sessionID
	f.got = append(f.got, act+":"+reply)
	return nil
}

func (f *fakeStore) ResolveSession(ctx context.Context, channel, ownerID string, gap time.Duration) (DispatchSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolvedChannel, f.resolvedOwner = channel, ownerID
	return f.session, nil
}

func (f *fakeStore) SessionTail(ctx context.Context, sessionID, sinceLogID int64, limit int) ([]DispatchTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tail, nil
}

func (f *fakeStore) SearchSessionSummaries(ctx context.Context, channel, ownerID, query string, limit int) ([]SessionSummaryHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recallHits, nil
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
	d, _, ex, _ := newTestDispatcher(`{"action":"send_whatsapp","params":{"phone":"60111","message":"hi"},"user_reply":"Sent to 60111."}`)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram"})
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if len(ex.sendCalls) != 1 || ex.sendCalls[0].Phone != "60111" || ex.sendCalls[0].Message != "hi" {
		t.Errorf("send calls: %+v", ex.sendCalls)
	}
	if res.UserReply != "Sent to 60111." {
		t.Errorf("reply should pass model text through: %s", res.UserReply)
	}
}

func TestRun_SendWhatsAppNoDuplicateStatus(t *testing.T) {
	// Regression: a one-shot send used to append the executor's "(sent to …)"
	// onto the model's reply, producing "Sent to Ana (60111). (sent to 60111)".
	d, _, ex, _ := newTestDispatcher(`{"action":"send_whatsapp","params":{"phone":"60111","message":"hi"},"user_reply":"Sent to Ana (60111)."}`)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram"})
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if len(ex.sendCalls) != 1 {
		t.Fatalf("send not dispatched: %+v", ex.sendCalls)
	}
	if strings.Contains(res.UserReply, "(sent to") {
		t.Errorf("duplicate executor status appended: %q", res.UserReply)
	}
	if res.UserReply != "Sent to Ana (60111)." {
		t.Errorf("reply should equal model text, got %q", res.UserReply)
	}
}

func TestRun_SendWhatsAppImage(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"send_whatsapp","params":{"phone":"60111","message":"see attached","image":"draft.png"},"user_reply":"Sent image."}`)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram"})
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if len(ex.sendCalls) != 0 {
		t.Errorf("text send should NOT have fired when image is set: %+v", ex.sendCalls)
	}
	if len(ex.sendImageCalls) != 1 {
		t.Fatalf("expected one image send, got %+v", ex.sendImageCalls)
	}
	c := ex.sendImageCalls[0]
	if c.Phone != "60111" || c.Image != "draft.png" || c.Caption != "see attached" {
		t.Errorf("image call args: %+v", c)
	}
}

func TestRun_SendWhatsAppImage_EmptyCaptionAllowed(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"send_whatsapp","params":{"phone":"60111","image":"chart.png"},"user_reply":"Sent."}`)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram"})
	if res.Error != "" {
		t.Fatalf("error: %s", res.Error)
	}
	if len(ex.sendImageCalls) != 1 || ex.sendImageCalls[0].Caption != "" {
		t.Errorf("expected image send with empty caption: %+v", ex.sendImageCalls)
	}
}

func TestRun_SendWhatsApp_RejectsEmptyBoth(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"send_whatsapp","params":{"phone":"60111"},"user_reply":"sending"}`)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram"})
	if res.Error == "" {
		t.Fatal("expected error when neither message nor image given")
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

func TestRun_ListCowork(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"list_cowork","params":{"date":"today"},"user_reply":"today's files:"}`)
	ex.coworkListResult = []CoworkFile{
		{Name: "broadcast_tan_140000.md", Date: "2026-05-19", Size: 240, IsText: true},
		{Name: "cover_140030.png", Date: "2026-05-19", Size: 51200, IsText: false},
	}
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1"})
	if res.Action != ActionListCowork {
		t.Errorf("action=%s", res.Action)
	}
	if !strings.Contains(res.UserReply, "broadcast_tan") {
		t.Errorf("expected file in reply: %s", res.UserReply)
	}
	if !strings.Contains(res.UserReply, "[txt]") || !strings.Contains(res.UserReply, "[bin]") {
		t.Errorf("expected [txt]/[bin] markers: %s", res.UserReply)
	}
	if len(ex.coworkListCalls) != 1 || ex.coworkListCalls[0] != "today" {
		t.Errorf("expected list call with date=today, got %v", ex.coworkListCalls)
	}
}

func TestRun_ListCoworkEmpty(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"list_cowork","params":{},"user_reply":"nothing today."}`)
	res := d.Run(context.Background(), DispatchInput{})
	if !strings.Contains(res.UserReply, "no files") {
		t.Errorf("expected '(no files)' marker: %s", res.UserReply)
	}
}

func TestRun_ReadCowork(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"read_cowork","params":{"filename":"draft_tan.md"},"user_reply":"here:"}`)
	ex.coworkReadResult = &CoworkRead{
		File:    CoworkFile{Name: "draft_tan.md", Date: "2026-05-19", Size: 42, IsText: true},
		Content: "Hi Tan WS — your renewal is ready.",
	}
	res := d.Run(context.Background(), DispatchInput{})
	if res.Action != ActionReadCowork {
		t.Errorf("action=%s", res.Action)
	}
	if !strings.Contains(res.UserReply, "Hi Tan WS") {
		t.Errorf("expected file body in reply: %s", res.UserReply)
	}
	if !strings.Contains(res.UserReply, "2026-05-19/draft_tan.md") {
		t.Errorf("expected file header: %s", res.UserReply)
	}
	if len(ex.coworkReadCalls) != 1 || ex.coworkReadCalls[0] != "draft_tan.md" {
		t.Errorf("expected one read call with that filename, got %v", ex.coworkReadCalls)
	}
}

func TestRun_ReadCoworkMissingFilename(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"read_cowork","params":{},"user_reply":"?"}`)
	res := d.Run(context.Background(), DispatchInput{})
	if res.Error == "" {
		t.Errorf("expected error when filename missing")
	}
}

func TestRun_SearchCowork(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"search_cowork","params":{"query":"tan","days":3},"user_reply":"found:"}`)
	ex.coworkSearchResult = []CoworkHit{
		{Date: "2026-05-19", Name: "draft.md", Line: 3, Snippet: "Hi Tan WS"},
	}
	res := d.Run(context.Background(), DispatchInput{})
	if !strings.Contains(res.UserReply, "draft.md:3") {
		t.Errorf("expected filename:line: %s", res.UserReply)
	}
	if !strings.Contains(res.UserReply, "Hi Tan WS") {
		t.Errorf("expected snippet: %s", res.UserReply)
	}
	if len(ex.coworkSearchCalls) != 1 || ex.coworkSearchCalls[0].Days != 3 {
		t.Errorf("expected days=3 in search call, got %+v", ex.coworkSearchCalls)
	}
}

func TestRun_SearchCoworkEmptyQuery(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"search_cowork","params":{"query":""},"user_reply":"?"}`)
	res := d.Run(context.Background(), DispatchInput{})
	if res.Error == "" {
		t.Errorf("expected error for empty query")
	}
}

func TestRun_EditCoworkAppend(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"edit_cowork","params":{"filename":"draft.md","op":"append","content":"P.S. extra"},"user_reply":"appended."}`)
	ex.coworkEditResult = &CoworkFile{Name: "draft.md", Date: "2026-05-19", Size: 100, IsText: true}
	res := d.Run(context.Background(), DispatchInput{})
	if res.Action != ActionEditCowork {
		t.Errorf("action=%s", res.Action)
	}
	if !strings.Contains(res.UserReply, "append") {
		t.Errorf("expected 'append' in status: %s", res.UserReply)
	}
	if len(ex.coworkEditCalls) != 1 {
		t.Errorf("expected 1 edit call, got %d", len(ex.coworkEditCalls))
	}
	if ex.coworkEditCalls[0].Content != "P.S. extra" {
		t.Errorf("content mismatch: %q", ex.coworkEditCalls[0].Content)
	}
}

func TestRun_EditCoworkRejectsEmptyContent(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"edit_cowork","params":{"filename":"draft.md","op":"append","content":""},"user_reply":"?"}`)
	res := d.Run(context.Background(), DispatchInput{})
	if res.Error == "" {
		t.Errorf("expected error for empty content")
	}
}

func TestRun_CreateCowork(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"create_cowork","params":{"filename":"notes_tan.md","date":"today","content":"hello"},"user_reply":"created."}`)
	ex.coworkCreateResult = &CoworkFile{Name: "notes_tan.md", Date: "2026-05-19", Size: 5, IsText: true}
	res := d.Run(context.Background(), DispatchInput{})
	if res.Action != ActionCreateCowork {
		t.Fatalf("action = %q", res.Action)
	}
	if len(ex.coworkCreateCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(ex.coworkCreateCalls))
	}
	c := ex.coworkCreateCalls[0]
	if c.Filename != "notes_tan.md" || c.Date != "today" || c.Content != "hello" {
		t.Errorf("unexpected create call %+v", c)
	}
}

func TestRun_CreateCoworkMissingFilename(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"create_cowork","params":{"content":"x"},"user_reply":"?"}`)
	res := d.Run(context.Background(), DispatchInput{})
	if res.Error == "" {
		t.Fatal("expected error when filename missing")
	}
}

func TestRun_CoworkPath(t *testing.T) {
	d, _, ex, _ := newTestDispatcher(`{"action":"cowork_path","params":{"date":"today"},"user_reply":"write here:"}`)
	ex.coworkPathResult = "/vault/Cowork/2026-05-20"
	res := d.Run(context.Background(), DispatchInput{})
	if res.Action != ActionCoworkPath {
		t.Errorf("action=%s", res.Action)
	}
	if !strings.Contains(res.UserReply, "/vault/Cowork/2026-05-20") {
		t.Errorf("expected path in reply: %s", res.UserReply)
	}
	if len(ex.coworkPathCalls) != 1 || ex.coworkPathCalls[0] != "today" {
		t.Errorf("expected one path call with date=today, got %v", ex.coworkPathCalls)
	}
}

func TestDispatchListKB(t *testing.T) {
	reply := `{"action":"list_kb","params":{"subdir":""},"user_reply":"Listing your KB folder."}`
	d, _, ex, _ := newTestDispatcher(reply)
	ex.kbEntries = []KBEntry{{Name: "report.md", IsText: true}, {Name: "imgs", IsDir: true}}
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "list my kb"})
	if res.Action != ActionListKB {
		t.Fatalf("want list_kb, got %s", res.Action)
	}
	if !strings.Contains(res.UserReply, "report.md") {
		t.Fatalf("reply should list files, got %q", res.UserReply)
	}
}

func TestDispatchReadKB(t *testing.T) {
	reply := `{"action":"read_kb","params":{"path":"report.md"},"user_reply":"Here it is:"}`
	d, _, ex, _ := newTestDispatcher(reply)
	ex.kbContent = "Q1 revenue up 12%."
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "read report.md"})
	if res.Action != ActionReadKB {
		t.Fatalf("want read_kb, got %s", res.Action)
	}
	if !strings.Contains(res.UserReply, "Q1 revenue") {
		t.Fatalf("reply should contain file content, got %q", res.UserReply)
	}
}

func TestDispatchReadNote(t *testing.T) {
	reply := `{"action":"read_note","params":{"name":"Alice"},"user_reply":"Note:"}`
	d, _, ex, _ := newTestDispatcher(reply)
	ex.note = &NoteView{Name: "Alice", Body: "VIP client", OutLinks: []string{"Tan Policy"}, Tags: []string{"vip"}}
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "open Alice"})
	if res.Action != ActionReadNote || !strings.Contains(res.UserReply, "VIP client") {
		t.Fatalf("unexpected result %s / %q", res.Action, res.UserReply)
	}
}

func TestDispatchBacklinks(t *testing.T) {
	reply := `{"action":"backlinks","params":{"name":"Tan Policy"},"user_reply":"Backlinks:"}`
	d, _, ex, _ := newTestDispatcher(reply)
	ex.backlinks = []string{"Clients/Alice", "Clients/Bob"}
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "who links to Tan Policy"})
	if res.Action != ActionBacklinks || !strings.Contains(res.UserReply, "Clients/Alice") {
		t.Fatalf("unexpected result %s / %q", res.Action, res.UserReply)
	}
}

func TestDispatchSearchNotes(t *testing.T) {
	reply := `{"action":"search_notes","params":{"query":"renewal"},"user_reply":"Found:"}`
	d, _, ex, _ := newTestDispatcher(reply)
	ex.noteHits = []NoteHit{{Note: "Clients/Alice", Line: 3, Snippet: "renewal due in March"}}
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "search renewal"})
	if res.Action != ActionSearchNotes || !strings.Contains(res.UserReply, "renewal due in March") {
		t.Fatalf("unexpected result %s / %q", res.Action, res.UserReply)
	}
}

func TestParseDispatchContinueFlag(t *testing.T) {
	p, err := parseDispatch(`{"action":"read_kb","params":{"path":"x.md"},"user_reply":"reading","continue":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Continue {
		t.Fatal("expected Continue=true")
	}
	p2, _ := parseDispatch(`{"action":"reply","params":{},"user_reply":"hi"}`)
	if p2.Continue {
		t.Fatal("expected Continue=false when omitted")
	}
}

func TestFakeClaudeSequence(t *testing.T) {
	c := &fakeClaude{replies: []string{"a", "b"}}
	if got, _ := c.Reply(context.Background(), "", ""); got != "a" {
		t.Fatalf("call 1 = %q, want a", got)
	}
	if got, _ := c.Reply(context.Background(), "", ""); got != "b" {
		t.Fatalf("call 2 = %q, want b", got)
	}
	// Past the end, repeats the last entry (avoids index panic).
	if got, _ := c.Reply(context.Background(), "", ""); got != "b" {
		t.Fatalf("call 3 = %q, want b (repeat last)", got)
	}
}

func TestRunChainsReadThenSend(t *testing.T) {
	// Step 1: read_kb with continue:true. Step 2: send_whatsapp (terminal).
	d, c, ex, _ := newTestDispatcherSeq(
		`{"action":"read_kb","params":{"path":"report.md"},"user_reply":"reading","continue":true}`,
		`{"action":"send_whatsapp","params":{"phone":"60123","message":"Q1 is up"},"user_reply":"Messaged Alice."}`,
	)
	ex.kbContent = "Q1 revenue up 12%."
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "read report.md and tell 60123"})

	// The read_kb result must have been fed back into the prompt for step 2.
	if !strings.Contains(c.lastU, "Q1 revenue up 12%") {
		t.Fatalf("read result not fed back into prompt; lastU=%q", c.lastU)
	}
	// The send actually happened.
	if len(ex.sendCalls) != 1 {
		t.Fatalf("want 1 send, got %d", len(ex.sendCalls))
	}
	// Final reply is the last action's message, with the action trail appended.
	if !strings.Contains(res.UserReply, "Messaged Alice") {
		t.Fatalf("unexpected final reply %q", res.UserReply)
	}
	if !strings.Contains(res.UserReply, "read_kb → send_whatsapp") {
		t.Fatalf("expected action trail in reply, got %q", res.UserReply)
	}
	// Two Claude calls (one per step).
	if c.calls != 2 {
		t.Fatalf("want 2 Claude calls, got %d", c.calls)
	}
}

func TestRunReplyTerminatesImmediately(t *testing.T) {
	d, c, _, _ := newTestDispatcherSeq(`{"action":"reply","params":{},"user_reply":"Hey!"}`)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "hi"})
	if res.Action != ActionReply || res.UserReply != "Hey!" {
		t.Fatalf("unexpected %s / %q", res.Action, res.UserReply)
	}
	if strings.Contains(res.UserReply, "→") {
		t.Fatalf("single reply should have no trail: %q", res.UserReply)
	}
	if c.calls != 1 {
		t.Fatalf("want 1 call, got %d", c.calls)
	}
}

func TestRunSingleActionOneShotUnchanged(t *testing.T) {
	// No continue flag → behaves one-shot: status appended, no trail, one call.
	d, c, ex, _ := newTestDispatcher(`{"action":"read_kb","params":{"path":"x.md"},"user_reply":"Here:"}`)
	ex.kbContent = "hello world"
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "read x.md"})
	if res.Action != ActionReadKB {
		t.Fatalf("want read_kb, got %s", res.Action)
	}
	if !strings.Contains(res.UserReply, "hello world") {
		t.Fatalf("one-shot read should append content, got %q", res.UserReply)
	}
	if strings.Contains(res.UserReply, "→") {
		t.Fatalf("single action should have no trail: %q", res.UserReply)
	}
	if c.calls != 1 {
		t.Fatalf("want 1 call, got %d", c.calls)
	}
}

func TestRunStepCapStops(t *testing.T) {
	// Model always asks to continue with a read; loop must stop at maxDispatchSteps.
	d, c, ex, _ := newTestDispatcherSeq(
		`{"action":"read_kb","params":{"path":"a.md"},"user_reply":"...","continue":true}`,
	)
	ex.kbContent = "x"
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "loop"})
	if c.calls != maxDispatchSteps {
		t.Fatalf("want %d calls (step cap), got %d", maxDispatchSteps, c.calls)
	}
	if res.UserReply == "" {
		t.Fatal("expected a non-empty final reply at the cap")
	}
}

func TestSystemPromptDocumentsContinue(t *testing.T) {
	if !strings.Contains(dispatchSystemPrompt, `"continue"`) {
		t.Fatal("system prompt must document the continue flag")
	}
	if !strings.Contains(dispatchSystemPrompt, "read_kb") {
		t.Fatal("system prompt should still list the actions")
	}
}

func TestRunResolvesSessionAndLogsIt(t *testing.T) {
	d, _, _, st := newTestDispatcherSeq(`{"action":"reply","params":{},"user_reply":"hi"}`)
	st.session = DispatchSession{ID: 42, Summary: "Earlier: discussed Tan policy.", SummaryThroughLogID: 7}
	st.tail = []DispatchTurn{{Message: "what next?", UserReply: "Renew in March.", ID: 8}}
	_ = d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "hello"})

	st.mu.Lock()
	rc, ro := st.resolvedChannel, st.resolvedOwner
	st.mu.Unlock()
	if rc != "telegram" || ro != "1" {
		t.Fatalf("session not resolved for owner: %q/%q", rc, ro)
	}
	waitForLog(t, st, 1)
	st.mu.Lock()
	sid := st.loggedSessionID
	st.mu.Unlock()
	if sid != 42 {
		t.Fatalf("log not tagged with session id, got %d", sid)
	}
}

func TestBuildPromptIncludesSummaryAndTail(t *testing.T) {
	sess := DispatchSession{Summary: "Rolling summary: client Tan wants renewal."}
	tail := []DispatchTurn{{Message: "remind me", UserReply: "Renewal due March."}}
	got := buildDispatchUserPrompt(DispatchInput{Channel: "telegram", OwnerID: "1", Message: "ok"}, sess, tail)
	if !strings.Contains(got, "Rolling summary: client Tan wants renewal.") {
		t.Fatalf("prompt missing summary: %q", got)
	}
	if !strings.Contains(got, "Renewal due March.") {
		t.Fatalf("prompt missing tail: %q", got)
	}
}

func TestRunRecallMemory(t *testing.T) {
	d, c, _, st := newTestDispatcherSeq(
		`{"action":"recall_memory","params":{"query":"tan policy"},"user_reply":"checking","continue":true}`,
		`{"action":"reply","params":{},"user_reply":"We agreed to renew in March."}`,
	)
	st.recallHits = []SessionSummaryHit{{SessionID: 9, Summary: "Owner agreed to renew Tan policy in March."}}
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "what did we decide about tan?"})
	if !strings.Contains(c.lastU, "renew Tan policy in March") {
		t.Fatalf("recalled summary not fed back into prompt: %q", c.lastU)
	}
	if !strings.Contains(res.UserReply, "March") {
		t.Fatalf("unexpected reply %q", res.UserReply)
	}
}

func TestRecallMemoryRequiresQuery(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"recall_memory","params":{},"user_reply":"x"}`)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "recall"})
	if !strings.Contains(strings.ToLower(res.UserReply), "query") {
		t.Fatalf("expected a 'query required' style failure, got %q", res.UserReply)
	}
}

func TestRun_InjectsOwnerProfile(t *testing.T) {
	runner := &fakeClaude{reply: `{"action":"reply","user_reply":"hi"}`}
	ex := &fakeExec{ownerProfile: "I am Dad, an insurance agent. Be formal."}
	d := NewDispatcher(runner, ex, nil)

	d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "hello"})

	if !strings.Contains(runner.lastS, "I am Dad, an insurance agent") {
		t.Fatalf("system prompt missing owner profile; got:\n%s", runner.lastS)
	}
	if !strings.Contains(runner.lastS, "Respond with ONE JSON object only") {
		t.Fatalf("system prompt dropped the action schema; got:\n%s", runner.lastS)
	}
}

func TestRun_NoProfile_SystemPromptUnchanged(t *testing.T) {
	runner := &fakeClaude{reply: `{"action":"reply","user_reply":"hi"}`}
	ex := &fakeExec{} // empty ownerProfile
	d := NewDispatcher(runner, ex, nil)

	d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "hello"})

	if strings.Contains(runner.lastS, "OWNER PROFILE") {
		t.Fatalf("empty profile should not inject a profile block; got:\n%s", runner.lastS)
	}
}

func TestRun_GetOwnerProfile(t *testing.T) {
	runner := &fakeClaude{reply: `{"action":"get_owner_profile","params":{},"user_reply":"Here it is:"}`}
	ex := &fakeExec{ownerProfile: "Dad, formal tone."}
	d := NewDispatcher(runner, ex, nil)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "show my profile"})
	if res.Action != ActionGetOwnerProfile {
		t.Fatalf("action: got %v", res.Action)
	}
	if !strings.Contains(res.UserReply, "Dad, formal tone.") {
		t.Fatalf("reply missing profile: %q", res.UserReply)
	}
}

func TestRun_UpdateOwnerProfile(t *testing.T) {
	runner := &fakeClaude{reply: `{"action":"update_owner_profile","params":{"content":"New bio","mode":"replace"},"user_reply":"Saved."}`}
	ex := &fakeExec{}
	d := NewDispatcher(runner, ex, nil)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "set my profile to New bio"})
	if res.Action != ActionUpdateOwnerProfile {
		t.Fatalf("action: got %v", res.Action)
	}
	if ex.updatedProfile != "New bio" || ex.updatedProfileMd != "replace" {
		t.Fatalf("not written: %q / %q", ex.updatedProfile, ex.updatedProfileMd)
	}
}

func TestRun_UpdateOwnerProfileRequiresContent(t *testing.T) {
	runner := &fakeClaude{reply: `{"action":"update_owner_profile","params":{"content":""},"user_reply":"ok"}`}
	d := NewDispatcher(runner, &fakeExec{}, nil)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "x"})
	if !strings.Contains(res.UserReply, "failed") {
		t.Fatalf("expected failure note, got %q", res.UserReply)
	}
}
