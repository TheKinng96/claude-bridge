// messenger_api.go implements Facebook Messenger using the official Graph API.
// This handles OAuth, reading conversations, sending messages, and polling.
// Browser automation is NOT used for Messenger — only for posting.
package facebook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"claude-bridge/internal/store"
)

const (
	graphAPIBase    = "https://graph.facebook.com/v21.0"
	oauthDialogURL  = "https://www.facebook.com/v21.0/dialog/oauth"
	oauthTokenURL   = graphAPIBase + "/oauth/access_token"
	oauthScopes     = "pages_messaging,pages_read_engagement,pages_manage_metadata"
	pollInterval    = 30 * time.Second
)

// OAuthConfig holds the user's Meta app credentials.
type OAuthConfig struct {
	AppID       string `json:"app_id"`
	AppSecret   string `json:"app_secret"`
	RedirectURI string `json:"redirect_uri"`
}

// PageToken holds a page's access token and metadata.
type PageToken struct {
	PageID      string `json:"page_id"`
	PageName    string `json:"page_name"`
	AccessToken string `json:"access_token"`
}

// MessengerAPI handles Messenger via the Graph API.
type MessengerAPI struct {
	mu          sync.RWMutex
	store       *store.Store
	logger      *log.Logger
	oauth       *OAuthConfig
	pageToken   *PageToken
	httpClient  *http.Client
	polling     bool
	stopPolling chan struct{}
}

// NewMessengerAPI creates a new Messenger API client.
func NewMessengerAPI(appStore *store.Store) *MessengerAPI {
	return &MessengerAPI{
		store:      appStore,
		logger:     log.Default(),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// ---------------------------------------------------------------------------
// OAuth Flow
// ---------------------------------------------------------------------------

// SetOAuthConfig saves the user's Meta app credentials.
func (m *MessengerAPI) SetOAuthConfig(ctx context.Context, appID, appSecret, redirectURI string) error {
	m.mu.Lock()
	m.oauth = &OAuthConfig{
		AppID:       appID,
		AppSecret:   appSecret,
		RedirectURI: redirectURI,
	}
	m.mu.Unlock()

	// Persist to credentials store
	extra, _ := json.Marshal(map[string]string{
		"app_id":       appID,
		"app_secret":   appSecret,
		"redirect_uri": redirectURI,
	})
	return m.store.SaveCredential(ctx, &store.Credential{
		Platform: "facebook_oauth",
		Email:    appID,
		Password: appSecret,
		Extra:    string(extra),
	})
}

// GetOAuthConfig returns the saved OAuth config.
func (m *MessengerAPI) GetOAuthConfig() *OAuthConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.oauth
}

// LoadOAuthConfig loads saved OAuth config from the store.
func (m *MessengerAPI) LoadOAuthConfig(ctx context.Context) error {
	cred, err := m.store.GetCredential(ctx, "facebook_oauth")
	if err != nil || cred == nil {
		return nil // no config saved yet, that's fine
	}

	var extra map[string]string
	json.Unmarshal([]byte(cred.Extra), &extra)

	m.mu.Lock()
	m.oauth = &OAuthConfig{
		AppID:       cred.Email,
		AppSecret:   cred.Password,
		RedirectURI: extra["redirect_uri"],
	}
	m.mu.Unlock()

	// Also load page token if saved
	pageCred, err := m.store.GetCredential(ctx, "facebook_page_token")
	if err == nil && pageCred != nil {
		var pageExtra map[string]string
		json.Unmarshal([]byte(pageCred.Extra), &pageExtra)
		m.mu.Lock()
		m.pageToken = &PageToken{
			PageID:      pageExtra["page_id"],
			PageName:    pageExtra["page_name"],
			AccessToken: pageCred.Password,
		}
		m.mu.Unlock()
	}

	return nil
}

// GetOAuthURL returns the Facebook OAuth dialog URL for the user to authorize.
func (m *MessengerAPI) GetOAuthURL(state string) (string, error) {
	m.mu.RLock()
	oauth := m.oauth
	m.mu.RUnlock()

	if oauth == nil || oauth.AppID == "" {
		return "", fmt.Errorf("OAuth not configured — set App ID and App Secret first")
	}

	params := url.Values{
		"client_id":    {oauth.AppID},
		"redirect_uri": {oauth.RedirectURI},
		"scope":        {oauthScopes},
		"state":        {state},
		"response_type": {"code"},
	}

	return oauthDialogURL + "?" + params.Encode(), nil
}

// HandleOAuthCallback exchanges the authorization code for tokens.
func (m *MessengerAPI) HandleOAuthCallback(ctx context.Context, code string) error {
	m.mu.RLock()
	oauth := m.oauth
	m.mu.RUnlock()

	if oauth == nil {
		return fmt.Errorf("OAuth not configured")
	}

	// Step 1: Exchange code for short-lived user token
	params := url.Values{
		"client_id":     {oauth.AppID},
		"client_secret": {oauth.AppSecret},
		"redirect_uri":  {oauth.RedirectURI},
		"code":          {code},
	}

	resp, err := m.httpClient.Get(oauthTokenURL + "?" + params.Encode())
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		Error       *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return fmt.Errorf("parse token response: %w", err)
	}
	if tokenResp.Error != nil {
		return fmt.Errorf("Facebook error: %s", tokenResp.Error.Message)
	}
	if tokenResp.AccessToken == "" {
		return fmt.Errorf("no access token in response")
	}

	shortToken := tokenResp.AccessToken

	// Step 2: Exchange for long-lived token (60 days)
	longToken, err := m.exchangeLongLivedToken(shortToken)
	if err != nil {
		m.logger.Printf("[messenger] Warning: could not get long-lived token, using short-lived: %v", err)
		longToken = shortToken
	}

	// Step 3: Get Page Access Token (doesn't expire)
	pageToken, err := m.getPageToken(longToken)
	if err != nil {
		return fmt.Errorf("get page token: %w", err)
	}

	m.mu.Lock()
	m.pageToken = pageToken
	m.mu.Unlock()

	// Save page token to store
	pageExtra, _ := json.Marshal(map[string]string{
		"page_id":   pageToken.PageID,
		"page_name": pageToken.PageName,
	})
	m.store.SaveCredential(ctx, &store.Credential{
		Platform: "facebook_page_token",
		Email:    pageToken.PageID,
		Password: pageToken.AccessToken,
		Extra:    string(pageExtra),
	})

	m.logger.Printf("[messenger] OAuth complete — connected to page: %s (%s)", pageToken.PageName, pageToken.PageID)
	return nil
}

func (m *MessengerAPI) exchangeLongLivedToken(shortToken string) (string, error) {
	m.mu.RLock()
	oauth := m.oauth
	m.mu.RUnlock()

	params := url.Values{
		"grant_type":        {"fb_exchange_token"},
		"client_id":         {oauth.AppID},
		"client_secret":     {oauth.AppSecret},
		"fb_exchange_token": {shortToken},
	}

	resp, err := m.httpClient.Get(oauthTokenURL + "?" + params.Encode())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
	}
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &result)

	if result.AccessToken == "" {
		return "", fmt.Errorf("no long-lived token returned")
	}
	return result.AccessToken, nil
}

func (m *MessengerAPI) getPageToken(userToken string) (*PageToken, error) {
	resp, err := m.httpClient.Get(graphAPIBase + "/me/accounts?fields=id,name,access_token&access_token=" + url.QueryEscape(userToken))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse pages response: %w", err)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no Facebook Pages found — make sure you have admin access to at least one Page")
	}

	// Use the first page (TODO: let user choose if multiple pages)
	page := result.Data[0]
	return &PageToken{
		PageID:      page.ID,
		PageName:    page.Name,
		AccessToken: page.AccessToken,
	}, nil
}

// IsConnected returns whether we have a valid page token.
func (m *MessengerAPI) IsConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pageToken != nil && m.pageToken.AccessToken != ""
}

// GetPageInfo returns the connected page info.
func (m *MessengerAPI) GetPageInfo() *PageToken {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pageToken
}

// Disconnect clears the page token.
func (m *MessengerAPI) Disconnect(ctx context.Context) {
	m.mu.Lock()
	m.pageToken = nil
	m.mu.Unlock()
	m.StopPolling()
	m.store.DeleteCredential(ctx, "facebook_page_token")
}

// ---------------------------------------------------------------------------
// Conversations & Messages (Graph API)
// ---------------------------------------------------------------------------

// GetConversations fetches conversations from the Page.
func (m *MessengerAPI) GetConversations(ctx context.Context, limit int) ([]store.CachedContact, error) {
	m.mu.RLock()
	pt := m.pageToken
	m.mu.RUnlock()

	if pt == nil {
		return nil, fmt.Errorf("not connected — complete OAuth first")
	}

	if limit <= 0 {
		limit = 25
	}

	apiURL := fmt.Sprintf("%s/%s/conversations?fields=id,senders,updated_time&limit=%d&access_token=%s",
		graphAPIBase, pt.PageID, limit, url.QueryEscape(pt.AccessToken))

	resp, err := m.httpClient.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("fetch conversations: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID          string    `json:"id"`
			UpdatedTime time.Time `json:"updated_time"`
			Senders     struct {
				Data []struct {
					ID    string `json:"id"`
					Name  string `json:"name"`
					Email string `json:"email"`
				} `json:"data"`
			} `json:"senders"`
		} `json:"data"`
	}

	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse conversations: %w — body: %s", err, string(body))
	}

	var contacts []store.CachedContact
	for _, conv := range result.Data {
		name := "Unknown"
		userID := ""
		if len(conv.Senders.Data) > 0 {
			// Find the sender that's NOT the page
			for _, s := range conv.Senders.Data {
				if s.ID != pt.PageID {
					name = s.Name
					userID = s.ID
					break
				}
			}
		}

		contact := store.CachedContact{
			Platform:   platformName,
			ContactID:  conv.ID,
			Name:       name,
			Username:   userID,
			ProfileURL: fmt.Sprintf("https://www.facebook.com/%s", userID),
		}
		contacts = append(contacts, contact)
		m.store.UpsertCachedContact(ctx, &contact)
	}

	return contacts, nil
}

// GetMessages fetches messages for a specific conversation.
func (m *MessengerAPI) GetMessages(ctx context.Context, conversationID string, limit int) ([]store.CachedMessage, error) {
	m.mu.RLock()
	pt := m.pageToken
	m.mu.RUnlock()

	if pt == nil {
		return nil, fmt.Errorf("not connected — complete OAuth first")
	}

	if limit <= 0 {
		limit = 50
	}

	apiURL := fmt.Sprintf("%s/%s/messages?fields=id,created_time,message,from&limit=%d&access_token=%s",
		graphAPIBase, conversationID, limit, url.QueryEscape(pt.AccessToken))

	resp, err := m.httpClient.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("fetch messages: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID          string    `json:"id"`
			CreatedTime time.Time `json:"created_time"`
			Message     string    `json:"message"`
			From        struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"from"`
		} `json:"data"`
	}

	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse messages: %w", err)
	}

	var messages []store.CachedMessage
	for _, msg := range result.Data {
		isOutgoing := msg.From.ID == pt.PageID
		cm := store.CachedMessage{
			Platform:       platformName,
			ConversationID: conversationID,
			MessageID:      msg.ID,
			SenderID:       msg.From.ID,
			SenderName:     msg.From.Name,
			Content:        msg.Message,
			Timestamp:      msg.CreatedTime,
			IsOutgoing:     isOutgoing,
		}
		messages = append(messages, cm)
	}

	// Cache messages
	for i := range messages {
		m.store.UpsertCachedMessage(ctx, &messages[i])
	}

	return messages, nil
}

// SendMessage sends a reply to a user via the Page.
func (m *MessengerAPI) SendMessage(ctx context.Context, recipientID, message string) error {
	m.mu.RLock()
	pt := m.pageToken
	m.mu.RUnlock()

	if pt == nil {
		return fmt.Errorf("not connected — complete OAuth first")
	}

	payload := map[string]interface{}{
		"recipient":      map[string]string{"id": recipientID},
		"message":        map[string]string{"text": message},
		"messaging_type": "RESPONSE",
	}

	payloadJSON, _ := json.Marshal(payload)
	apiURL := fmt.Sprintf("%s/%s/messages?access_token=%s",
		graphAPIBase, pt.PageID, url.QueryEscape(pt.AccessToken))

	resp, err := m.httpClient.Post(apiURL, "application/json", strings.NewReader(string(payloadJSON)))
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("send failed (status %d): %s", resp.StatusCode, string(body))
	}

	// Cache the sent message
	m.store.UpsertCachedMessage(ctx, &store.CachedMessage{
		Platform:       platformName,
		ConversationID: recipientID,
		MessageID:      fmt.Sprintf("sent_%d", time.Now().UnixMilli()),
		SenderID:       pt.PageID,
		SenderName:     pt.PageName,
		Content:        message,
		Timestamp:      time.Now(),
		IsOutgoing:     true,
	})

	m.logger.Printf("[messenger] Message sent to %s", recipientID)
	return nil
}

// ---------------------------------------------------------------------------
// Background Polling
// ---------------------------------------------------------------------------

// StartPolling begins polling for new messages every 30 seconds.
func (m *MessengerAPI) StartPolling(ctx context.Context) {
	m.mu.Lock()
	if m.polling {
		m.mu.Unlock()
		return
	}
	m.polling = true
	m.stopPolling = make(chan struct{})
	m.mu.Unlock()

	m.logger.Printf("[messenger] Starting message polling (every %v)", pollInterval)

	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-m.stopPolling:
				m.logger.Println("[messenger] Polling stopped")
				return
			case <-ticker.C:
				m.pollOnce(ctx)
			}
		}
	}()
}

// StopPolling stops the background polling.
func (m *MessengerAPI) StopPolling() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.polling {
		close(m.stopPolling)
		m.polling = false
	}
}

// IsPolling returns whether message polling is active.
func (m *MessengerAPI) IsPolling() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.polling
}

// ---------------------------------------------------------------------------
// Posts (Graph API)
// ---------------------------------------------------------------------------

// GetPosts fetches recent posts from the Page.
func (m *MessengerAPI) GetPosts(ctx context.Context, limit int) ([]store.CachedPost, error) {
	m.mu.RLock()
	pt := m.pageToken
	m.mu.RUnlock()

	if pt == nil {
		return nil, fmt.Errorf("not connected — complete OAuth first")
	}

	if limit <= 0 {
		limit = 20
	}

	apiURL := fmt.Sprintf("%s/%s/posts?fields=id,message,created_time,permalink_url,shares,likes.summary(true),comments.summary(true)&limit=%d&access_token=%s",
		graphAPIBase, pt.PageID, limit, url.QueryEscape(pt.AccessToken))

	resp, err := m.httpClient.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("fetch posts: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID          string    `json:"id"`
			Message     string    `json:"message"`
			CreatedTime time.Time `json:"created_time"`
			PermalinkURL string   `json:"permalink_url"`
			Shares      *struct {
				Count int `json:"count"`
			} `json:"shares"`
			Likes struct {
				Summary struct {
					TotalCount int `json:"total_count"`
				} `json:"summary"`
			} `json:"likes"`
			Comments struct {
				Summary struct {
					TotalCount int `json:"total_count"`
				} `json:"summary"`
			} `json:"comments"`
		} `json:"data"`
	}

	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse posts: %w — body: %s", err, string(body))
	}

	var posts []store.CachedPost
	for _, p := range result.Data {
		shares := 0
		if p.Shares != nil {
			shares = p.Shares.Count
		}
		post := store.CachedPost{
			Platform: platformName,
			PostID:   p.ID,
			Content:  p.Message,
			PostURL:  p.PermalinkURL,
			PostedAt: p.CreatedTime,
			Likes:    p.Likes.Summary.TotalCount,
			Comments: p.Comments.Summary.TotalCount,
			Shares:   shares,
		}
		posts = append(posts, post)
		m.store.UpsertCachedPost(ctx, &post)
	}

	return posts, nil
}

// GetComments fetches comments on a specific post.
func (m *MessengerAPI) GetComments(ctx context.Context, postID string, limit int) ([]store.CachedComment, error) {
	m.mu.RLock()
	pt := m.pageToken
	m.mu.RUnlock()

	if pt == nil {
		return nil, fmt.Errorf("not connected — complete OAuth first")
	}

	if limit <= 0 {
		limit = 50
	}

	apiURL := fmt.Sprintf("%s/%s/comments?fields=id,from,message,created_time,like_count&limit=%d&access_token=%s",
		graphAPIBase, postID, limit, url.QueryEscape(pt.AccessToken))

	resp, err := m.httpClient.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("fetch comments: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID          string    `json:"id"`
			Message     string    `json:"message"`
			CreatedTime time.Time `json:"created_time"`
			LikeCount   int       `json:"like_count"`
			From        struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"from"`
		} `json:"data"`
	}

	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse comments: %w — body: %s", err, string(body))
	}

	var comments []store.CachedComment
	for _, c := range result.Data {
		comment := store.CachedComment{
			Platform:   platformName,
			PostID:     postID,
			CommentID:  c.ID,
			AuthorID:   c.From.ID,
			AuthorName: c.From.Name,
			Content:    c.Message,
			Likes:      c.LikeCount,
			Timestamp:  c.CreatedTime,
		}
		comments = append(comments, comment)
		m.store.UpsertCachedComment(ctx, &comment)
	}

	return comments, nil
}

// ReplyToComment posts a reply to a comment.
func (m *MessengerAPI) ReplyToComment(ctx context.Context, commentID, message string) error {
	m.mu.RLock()
	pt := m.pageToken
	m.mu.RUnlock()

	if pt == nil {
		return fmt.Errorf("not connected — complete OAuth first")
	}

	apiURL := fmt.Sprintf("%s/%s/comments?access_token=%s",
		graphAPIBase, commentID, url.QueryEscape(pt.AccessToken))

	payload, _ := json.Marshal(map[string]string{"message": message})
	resp, err := m.httpClient.Post(apiURL, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("reply to comment: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("reply failed (status %d): %s", resp.StatusCode, string(body))
	}

	m.logger.Printf("[messenger] Replied to comment %s", commentID)
	return nil
}

func (m *MessengerAPI) pollOnce(ctx context.Context) {
	conversations, err := m.GetConversations(ctx, 10)
	if err != nil {
		m.logger.Printf("[messenger] Poll error (conversations): %v", err)
		return
	}

	// Fetch latest messages for each conversation
	for _, conv := range conversations {
		msgs, err := m.GetMessages(ctx, conv.ContactID, 5)
		if err != nil {
			m.logger.Printf("[messenger] Poll error (messages for %s): %v", conv.ContactID, err)
			continue
		}
		if len(msgs) > 0 {
			m.logger.Printf("[messenger] Polled %d messages from %s", len(msgs), conv.Name)
		}
	}
}
