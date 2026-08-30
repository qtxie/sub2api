package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type weChatILinkDoFunc func(*http.Request) (*http.Response, error)

func (f weChatILinkDoFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func weChatILinkJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestWeChatILinkGetQRCodeUsesRequiredAppHeaders(t *testing.T) {
	client := &WeChatILinkClient{httpClient: weChatILinkDoFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", req.Method)
		}
		if got := req.URL.String(); got != weChatILinkBaseURL+"/ilink/bot/get_bot_qrcode?bot_type=3" {
			t.Fatalf("url = %q", got)
		}
		if got := req.Header.Get("iLink-App-Id"); got != weChatILinkAppID {
			t.Fatalf("iLink-App-Id = %q", got)
		}
		if got := req.Header.Get("iLink-App-ClientVersion"); got != weChatILinkClient {
			t.Fatalf("iLink-App-ClientVersion = %q", got)
		}
		return weChatILinkJSONResponse(`{"qrcode":"qr-id","qrcode_img_content":"https://example.invalid/qr"}`), nil
	})}

	result, err := client.GetQRCode(context.Background())
	if err != nil {
		t.Fatalf("GetQRCode() error = %v", err)
	}
	if result.QRCode != "qr-id" {
		t.Fatalf("QRCode = %q", result.QRCode)
	}
}

func TestWeChatILinkGetUpdatesCarriesCursorAndParsesText(t *testing.T) {
	client := &WeChatILinkClient{httpClient: weChatILinkDoFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != "https://worker.weixin.qq.com/ilink/bot/getupdates" {
			t.Fatalf("url = %q", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer bot-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := req.Header.Get("AuthorizationType"); got != "ilink_bot_token" {
			t.Fatalf("AuthorizationType = %q", got)
		}
		if req.Header.Get("X-WECHAT-UIN") == "" {
			t.Fatal("X-WECHAT-UIN is empty")
		}
		var payload struct {
			Cursor   string `json:"get_updates_buf"`
			BaseInfo struct {
				ChannelVersion string `json:"channel_version"`
			} `json:"base_info"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Cursor != "cursor-1" || payload.BaseInfo.ChannelVersion != weChatILinkChannel {
			t.Fatalf("payload = %+v", payload)
		}
		return weChatILinkJSONResponse(`{
			"ret":0,
			"get_updates_buf":"cursor-2",
			"msgs":[{
				"message_type":1,
				"from_user_id":"wx-user",
				"context_token":"ctx",
				"item_list":[
					{"type":1,"text_item":{"text":" hello "}},
					{"type":2,"text_item":{"text":"ignored"}},
					{"type":1,"text_item":{"text":"world"}}
				]
			}]
		}`), nil
	})}

	messages, cursor, err := client.GetUpdates(context.Background(), "https://worker.weixin.qq.com", "bot-token", "cursor-1")
	if err != nil {
		t.Fatalf("GetUpdates() error = %v", err)
	}
	if cursor != "cursor-2" || len(messages) != 1 {
		t.Fatalf("cursor = %q messages = %d", cursor, len(messages))
	}
	if got := messages[0].Text(); got != "hello \nworld" {
		t.Fatalf("Text() = %q", got)
	}
}

func TestWeChatILinkSendTextPayloadAndWindowError(t *testing.T) {
	requestCount := 0
	client := &WeChatILinkClient{httpClient: weChatILinkDoFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		var payload struct {
			Message struct {
				ToUserID     string `json:"to_user_id"`
				MessageType  int    `json:"message_type"`
				MessageState int    `json:"message_state"`
				ContextToken string `json:"context_token"`
				ClientID     string `json:"client_id"`
				Items        []struct {
					Type int `json:"type"`
					Text struct {
						Value string `json:"text"`
					} `json:"text_item"`
				} `json:"item_list"`
			} `json:"msg"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		msg := payload.Message
		if msg.ToUserID != "wx-user" || msg.MessageType != 2 || msg.MessageState != 2 || msg.ContextToken != "ctx" {
			t.Fatalf("message = %+v", msg)
		}
		if !strings.HasPrefix(msg.ClientID, "sub2api:") || len(msg.Items) != 1 || msg.Items[0].Type != 1 || msg.Items[0].Text.Value != "hello" {
			t.Fatalf("message payload is incomplete: %+v", msg)
		}
		if requestCount == 2 && msg.ClientID != "sub2api:outbox:42" {
			t.Fatalf("stable client_id = %q", msg.ClientID)
		}
		if requestCount == 1 {
			return weChatILinkJSONResponse(`{"ret":0}`), nil
		}
		return weChatILinkJSONResponse(`{"ret":-2}`), nil
	})}

	if err := client.SendText(context.Background(), weChatILinkBaseURL, "token", "wx-user", "hello", "ctx"); err != nil {
		t.Fatalf("SendText() error = %v", err)
	}
	if err := client.SendTextWithClientID(context.Background(), weChatILinkBaseURL, "token", "wx-user", "hello", "ctx", "sub2api:outbox:42"); !errors.Is(err, ErrWeChatILinkWindow) {
		t.Fatalf("SendText() error = %v, want ErrWeChatILinkWindow", err)
	}
}

func TestNormalizeWeChatILinkURL(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "empty default", want: weChatILinkBaseURL},
		{name: "trusted subdomain", value: "https://worker.weixin.qq.com/path", want: "https://worker.weixin.qq.com"},
		{name: "reject lookalike", value: "https://weixin.qq.com.example.org", wantErr: true},
		{name: "reject http", value: "http://worker.weixin.qq.com", wantErr: true},
		{name: "reject credentials", value: "https://user@worker.weixin.qq.com", wantErr: true},
		{name: "reject port", value: "https://worker.weixin.qq.com:8443", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeWeChatILinkURL(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr = %t", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("url = %q, want %q", got, tt.want)
			}
		})
	}
}
