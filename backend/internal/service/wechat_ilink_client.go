package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	weChatILinkBaseURL      = "https://ilinkai.weixin.qq.com"
	weChatILinkChannel      = "2.1.7"
	weChatILinkAppID        = "bot"
	weChatILinkClient       = "131335"
	weChatILinkTextItemType = 1
)

var (
	ErrWeChatILinkAuthExpired = errors.New("wechat ilink authentication expired")
	ErrWeChatILinkWindow      = errors.New("wechat ilink delivery window is closed")
)

type WeChatILinkQRCode struct {
	QRCode             string `json:"qrcode"`
	QRCodeImageContent string `json:"qrcode_img_content"`
	URL                string `json:"url"`
}

type WeChatILinkQRStatus struct {
	Status       string `json:"status"`
	RedirectHost string `json:"redirect_host"`
	BotToken     string `json:"bot_token"`
	BaseURL      string `json:"baseurl"`
	BotID        string `json:"ilink_bot_id"`
	UserID       string `json:"ilink_user_id"`
}

type WeChatILinkMessage struct {
	MessageType      int                      `json:"message_type"`
	MessageState     int                      `json:"message_state"`
	MessageID        string                   `json:"msg_id"`
	FromUserID       string                   `json:"from_user_id"`
	FromUserNickname string                   `json:"from_user_nickname"`
	FromUserName     string                   `json:"from_user_name"`
	ContextToken     string                   `json:"context_token"`
	Items            []WeChatILinkMessageItem `json:"item_list"`
}

type WeChatILinkMessageItem struct {
	Type     int `json:"type"`
	TextItem struct {
		Text string `json:"text"`
	} `json:"text_item"`
}

func (m WeChatILinkMessage) Text() string {
	parts := make([]string, 0, len(m.Items))
	for _, item := range m.Items {
		if item.Type == weChatILinkTextItemType && strings.TrimSpace(item.TextItem.Text) != "" {
			parts = append(parts, item.TextItem.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

type weChatILinkHTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type WeChatILinkClient struct {
	httpClient weChatILinkHTTPClient
}

func NewWeChatILinkClient() *WeChatILinkClient {
	return &WeChatILinkClient{httpClient: &http.Client{
		Timeout:       50 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *WeChatILinkClient) GetQRCode(ctx context.Context) (*WeChatILinkQRCode, error) {
	endpoint := weChatILinkBaseURL + "/ilink/bot/get_bot_qrcode?bot_type=3"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	setWeChatILinkAppHeaders(req.Header)
	var result WeChatILinkQRCode
	if err := c.doJSON(req, &result); err != nil {
		return nil, err
	}
	if strings.TrimSpace(result.QRCode) == "" {
		return nil, errors.New("wechat ilink returned an empty QR code")
	}
	return &result, nil
}

func (c *WeChatILinkClient) PollQRCode(ctx context.Context, baseURL, qrcode string) (*WeChatILinkQRStatus, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = weChatILinkBaseURL
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	setWeChatILinkAppHeaders(req.Header)
	var result WeChatILinkQRStatus
	if err := c.doJSON(req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *WeChatILinkClient) GetUpdates(ctx context.Context, baseURL, botToken, cursor string) ([]WeChatILinkMessage, string, error) {
	var result struct {
		Ret           int                  `json:"ret"`
		ErrCode       json.RawMessage      `json:"errcode"`
		ErrorMessage  string               `json:"errmsg"`
		GetUpdatesBuf string               `json:"get_updates_buf"`
		Messages      []WeChatILinkMessage `json:"msgs"`
	}
	err := c.postJSON(ctx, baseURL, "/ilink/bot/getupdates", botToken, map[string]any{
		"get_updates_buf": cursor,
	}, &result)
	if err != nil {
		return nil, cursor, err
	}
	if result.Ret != 0 || rawErrorCodeNonZero(result.ErrCode) {
		if result.Ret == -1 || result.Ret == http.StatusUnauthorized || result.Ret == http.StatusForbidden || rawErrorCodeAuthExpired(result.ErrCode) {
			return nil, cursor, ErrWeChatILinkAuthExpired
		}
		return nil, cursor, fmt.Errorf("wechat ilink getupdates failed: ret=%d errcode=%s", result.Ret, boundedWeChatILinkErrorCode(result.ErrCode))
	}
	return result.Messages, result.GetUpdatesBuf, nil
}

func (c *WeChatILinkClient) SendText(ctx context.Context, baseURL, botToken, toUserID, text, contextToken string) error {
	return c.SendTextWithClientID(ctx, baseURL, botToken, toUserID, text, contextToken, "")
}

func (c *WeChatILinkClient) SendTextWithClientID(ctx context.Context, baseURL, botToken, toUserID, text, contextToken, clientID string) error {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientIDBytes := make([]byte, 4)
		_, _ = rand.Read(clientIDBytes)
		clientID = fmt.Sprintf("sub2api:%d-%x", time.Now().UnixMilli(), clientIDBytes)
	}
	payload := map[string]any{"msg": map[string]any{
		"from_user_id":  "",
		"to_user_id":    toUserID,
		"client_id":     clientID,
		"message_type":  2,
		"message_state": 2,
		"context_token": contextToken,
		"item_list": []any{map[string]any{
			"type":      weChatILinkTextItemType,
			"text_item": map[string]any{"text": text},
		}},
	}}
	var result struct {
		Ret     int             `json:"ret"`
		ErrCode json.RawMessage `json:"errcode"`
		ErrMsg  string          `json:"errmsg"`
	}
	if err := c.postJSON(ctx, baseURL, "/ilink/bot/sendmessage", botToken, payload, &result); err != nil {
		return err
	}
	if result.Ret == -2 {
		return ErrWeChatILinkWindow
	}
	if result.Ret != 0 || rawErrorCodeNonZero(result.ErrCode) {
		if result.Ret == -1 || result.Ret == http.StatusUnauthorized || result.Ret == http.StatusForbidden || rawErrorCodeAuthExpired(result.ErrCode) {
			return ErrWeChatILinkAuthExpired
		}
		return fmt.Errorf("wechat ilink sendmessage failed: ret=%d errcode=%s", result.Ret, boundedWeChatILinkErrorCode(result.ErrCode))
	}
	return nil
}

func (c *WeChatILinkClient) postJSON(ctx context.Context, baseURL, path, botToken string, payload map[string]any, out any) error {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = weChatILinkBaseURL
	}
	payload["base_info"] = map[string]any{"channel_version": weChatILinkChannel}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", randomWeChatUIN())
	setWeChatILinkAppHeaders(req.Header)
	if botToken != "" {
		req.Header.Set("Authorization", "Bearer "+botToken)
	}
	return c.doJSON(req, out)
}

func (c *WeChatILinkClient) doJSON(req *http.Request, out any) error {
	client := c.httpClient
	if client == nil {
		client = NewWeChatILinkClient().httpClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrWeChatILinkAuthExpired
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("wechat ilink returned HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode wechat ilink response: %w", err)
	}
	return nil
}

func setWeChatILinkAppHeaders(header http.Header) {
	header.Set("iLink-App-Id", weChatILinkAppID)
	header.Set("iLink-App-ClientVersion", weChatILinkClient)
}

func randomWeChatUIN() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return base64.StdEncoding.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	}
	value := binary.LittleEndian.Uint32(buf)
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(value), 10)))
}

func rawErrorCodeNonZero(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "0" && trimmed != `"0"` && trimmed != "null"
}

func rawErrorCodeAuthExpired(raw json.RawMessage) bool {
	value := strings.ToLower(strings.Trim(strings.TrimSpace(string(raw)), `"`))
	return value == "-14" || value == "401" || value == "403" || value == "tokenexpired"
}

func boundedWeChatILinkErrorCode(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if len(value) > 80 {
		value = value[:80]
	}
	return value
}
