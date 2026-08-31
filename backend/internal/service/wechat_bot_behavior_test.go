package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type capturedWeChatNotification struct {
	userID    int64
	eventType string
	message   string
}

type weChatNotifierCapture struct {
	events []capturedWeChatNotification
}

func (n *weChatNotifierCapture) NotifyUser(_ context.Context, userID int64, eventType, message string) {
	n.events = append(n.events, capturedWeChatNotification{userID: userID, eventType: eventType, message: message})
}

type weChatSettingRepoStub struct {
	SettingRepository
	values map[string]string
}

func (s *weChatSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *weChatSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		result[key] = s.values[key]
	}
	return result, nil
}

type weChatUserRepoStub struct {
	UserRepository
	user *User
}

func (s *weChatUserRepoStub) GetByID(_ context.Context, _ int64) (*User, error) {
	return s.user, nil
}

type weChatAPIKeyRepoStub struct {
	APIKeyRepository
	key *APIKey
}

func (s *weChatAPIKeyRepoStub) GetByID(_ context.Context, _ int64) (*APIKey, error) {
	return s.key, nil
}

type weChatCommandRepoStub struct {
	WeChatBotRepository
	updated *WeChatBotSettingsUpdate
}

type weChatInboundRepoStub struct {
	WeChatBotRepository
	account  *WeChatBotAccount
	outbound int
}

func (r *weChatInboundRepoStub) TouchInbound(_ context.Context, _ int64, ilinkUserID, encryptedContextToken string, inboundAt time.Time) (*WeChatBotAccount, error) {
	r.account.ILinkUserID = ilinkUserID
	r.account.ContextTokenEncrypted = encryptedContextToken
	r.account.LastInboundAt = &inboundAt
	r.account.OutboundCount = 0
	return r.account, nil
}

func (r *weChatInboundRepoStub) IncrementOutbound(_ context.Context, _ int64, _ time.Time) error {
	r.outbound++
	return nil
}

type weChatLoginRepoStub struct {
	WeChatBotRepository
	mu      sync.Mutex
	saved   int
	deleted int
}

func (r *weChatLoginRepoStub) SaveLogin(_ context.Context, _ int64, _ *WeChatBotAccount) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saved++
	return nil
}

func (r *weChatLoginRepoStub) DeleteAccount(_ context.Context, _ int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted++
	return nil
}

type weChatLoginEncryptor struct{}

func (weChatLoginEncryptor) Encrypt(value string) (string, error) { return "enc:" + value, nil }
func (weChatLoginEncryptor) Decrypt(value string) (string, error) {
	plain, ok := strings.CutPrefix(value, "enc:")
	if !ok {
		return "", errors.New("invalid ciphertext")
	}
	return plain, nil
}

func (s *weChatCommandRepoStub) UpdateAccountSettings(_ context.Context, _ int64, update WeChatBotSettingsUpdate) error {
	s.updated = &update
	return nil
}

func (s *weChatCommandRepoStub) GetAccount(_ context.Context, _ int64) (*WeChatBotAccount, error) {
	return nil, ErrWeChatBotAccountMissing
}

func TestRecordSuccessfulLoginNotifiesWeChat(t *testing.T) {
	notifier := &weChatNotifierCapture{}
	svc := &AuthService{loginNotifier: notifier}

	svc.RecordSuccessfulLogin(context.Background(), 42)

	if len(notifier.events) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifier.events))
	}
	event := notifier.events[0]
	if event.userID != 42 || event.eventType != WeChatBotEventLogin || !strings.Contains(event.message, "登录通知") {
		t.Fatalf("notification = %+v", event)
	}
}

func TestBalanceCrossingNotifiesWeChatWithoutEmailService(t *testing.T) {
	notifier := &weChatNotifierCapture{}
	svc := &BalanceNotifyService{
		settingRepo: &weChatSettingRepoStub{values: map[string]string{
			SettingKeyBalanceLowNotifyEnabled:     "true",
			SettingKeyBalanceLowNotifyThreshold:   "10",
			SettingKeyBalanceLowNotifyRechargeURL: "https://example.com/recharge",
		}},
		wechatNotifier: notifier,
	}
	// WeChat balance alerts are independent from the legacy email preference.
	user := &User{ID: 7, BalanceNotifyEnabled: false}

	svc.CheckBalanceAfterDeduction(context.Background(), user, 12, 3)

	if len(notifier.events) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifier.events))
	}
	event := notifier.events[0]
	if event.userID != 7 || event.eventType != WeChatBotEventBalance {
		t.Fatalf("notification = %+v", event)
	}
	for _, expected := range []string{"$9.0000", "$10.0000", "https://example.com/recharge"} {
		if !strings.Contains(event.message, expected) {
			t.Fatalf("message %q does not contain %q", event.message, expected)
		}
	}
}

func TestWeChatBotStatusDefaultsProactiveNotificationsOff(t *testing.T) {
	svc := &WeChatBotService{repo: &weChatCommandRepoStub{}}

	status, err := svc.GetUserStatus(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetUserStatus() error = %v", err)
	}
	if status.NotifyAdmin || status.NotifyBalance || status.NotifyLogin {
		t.Fatalf(
			"proactive notification defaults = admin:%t balance:%t login:%t",
			status.NotifyAdmin, status.NotifyBalance, status.NotifyLogin,
		)
	}
}

func TestWeChatBotBalanceCommandAndOwnedKeySelection(t *testing.T) {
	repo := &weChatCommandRepoStub{}
	svc := &WeChatBotService{
		repo:       repo,
		userRepo:   &weChatUserRepoStub{user: &User{ID: 8, Balance: 12.3456, FrozenBalance: 1.25}},
		apiKeyRepo: &weChatAPIKeyRepoStub{key: &APIKey{ID: 33, UserID: 8, Name: "chat", Status: StatusActive}},
	}
	account := &WeChatBotAccount{UserID: 8, Enabled: true, ChatModel: "gpt-4o-mini"}

	if got := svc.handleCommand(context.Background(), account, "/balance"); got != "账户余额：$12.3456\n冻结余额：$1.2500" {
		t.Fatalf("balance response = %q", got)
	}
	if got := svc.handleCommand(context.Background(), account, "/key 33"); !strings.Contains(got, "已选择 API Key 33") {
		t.Fatalf("key response = %q", got)
	}
	if account.ChatAPIKeyID == nil || *account.ChatAPIKeyID != 33 || !account.ChatEnabled || repo.updated == nil {
		t.Fatalf("account was not updated: %+v", account)
	}

	svc.apiKeyRepo = &weChatAPIKeyRepoStub{key: &APIKey{ID: 44, UserID: 99, Status: StatusActive}}
	if got := svc.handleCommand(context.Background(), account, "/key 44"); !strings.Contains(got, "不属于当前账号") {
		t.Fatalf("foreign key response = %q", got)
	}
}

func TestWeChatBotExplainsWhenChatIsDisabled(t *testing.T) {
	now := time.Now().UTC()
	repo := &weChatInboundRepoStub{account: &WeChatBotAccount{
		UserID:            7,
		BotTokenEncrypted: "enc:bot-token",
		BaseURL:           weChatILinkBaseURL,
		ILinkUserID:       "wx-user",
		Enabled:           true,
		ChatEnabled:       false,
		LastInboundAt:     &now,
	}}
	var sent string
	svc := &WeChatBotService{
		ctx:       context.Background(),
		repo:      repo,
		encryptor: weChatLoginEncryptor{},
		ilink: &WeChatILinkClient{httpClient: weChatILinkDoFunc(func(req *http.Request) (*http.Response, error) {
			var payload struct {
				Message struct {
					Items []struct {
						Text struct {
							Value string `json:"text"`
						} `json:"text_item"`
					} `json:"item_list"`
				} `json:"msg"`
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			sent = payload.Message.Items[0].Text.Value
			return weChatILinkJSONResponse(`{"ret":0}`), nil
		})},
	}
	var item WeChatILinkMessageItem
	item.Type = weChatILinkTextItemType
	item.TextItem.Text = "hello"
	svc.handleInboundMessage(7, WeChatILinkMessage{
		MessageType:  1,
		FromUserID:   "wx-user",
		ContextToken: "context-token",
		Items:        []WeChatILinkMessageItem{item},
	})

	if !strings.Contains(sent, "API Key 聊天尚未开启") {
		t.Fatalf("reply = %q", sent)
	}
	if repo.outbound != 1 {
		t.Fatalf("outbound count = %d, want 1", repo.outbound)
	}
}

func TestWeChatBotLoginSessionIsOwnedByUser(t *testing.T) {
	expiresAt := time.Now().Add(time.Minute)
	svc := &WeChatBotService{logins: map[string]*weChatBotLoginSession{
		"login-1": {UserID: 7, QRCode: "qr", ExpiresAt: expiresAt},
	}}

	result, err := svc.PollLoginQR(context.Background(), 8, "login-1")
	if err != nil || result.Status != "expired" {
		t.Fatalf("PollLoginQR() = %+v, %v", result, err)
	}
	if svc.logins["login-1"] == nil {
		t.Fatal("another user invalidated the owned login session")
	}
}

func TestWeChatBotDisconnectRevokesInFlightLogin(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	repo := &weChatLoginRepoStub{}
	svc := &WeChatBotService{
		repo:      repo,
		encryptor: weChatLoginEncryptor{},
		ilink: &WeChatILinkClient{httpClient: weChatILinkDoFunc(func(_ *http.Request) (*http.Response, error) {
			close(requestStarted)
			<-releaseResponse
			return weChatILinkJSONResponse(`{
				"status":"confirmed",
				"bot_token":"token",
				"ilink_bot_id":"bot-1",
				"ilink_user_id":"wx-user-1",
				"baseurl":"https://ilinkai.weixin.qq.com"
			}`), nil
		})},
		logins: map[string]*weChatBotLoginSession{
			"login-1": {UserID: 7, QRCode: "qr", BaseURL: weChatILinkBaseURL, ExpiresAt: time.Now().Add(time.Minute)},
		},
	}

	type pollResult struct {
		status *WeChatBotLoginStatus
		err    error
	}
	result := make(chan pollResult, 1)
	go func() {
		status, err := svc.PollLoginQR(context.Background(), 7, "login-1")
		result <- pollResult{status: status, err: err}
	}()

	<-requestStarted
	if err := svc.Disconnect(context.Background(), 7); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	close(releaseResponse)
	poll := <-result
	if poll.err != nil || poll.status.Status != "expired" {
		t.Fatalf("PollLoginQR() = %+v, %v", poll.status, poll.err)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.saved != 0 || repo.deleted != 1 {
		t.Fatalf("saved = %d, deleted = %d", repo.saved, repo.deleted)
	}
}
