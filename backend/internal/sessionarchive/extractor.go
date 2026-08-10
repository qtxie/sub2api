package sessionarchive

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

type RemoteFetcher interface {
	Fetch(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error)
}

type Extractor struct {
	MaxPartBytes int64
	Fetcher      RemoteFetcher
}

func (e *Extractor) Extract(ctx context.Context, body []byte, contentType string) ([]Turn, error) {
	mediaType, params, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(mediaType, "multipart/") {
		return e.extractMultipart(body, params["boundary"])
	}
	var root any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode session archive request: %w", err)
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("session archive request must be a JSON object")
	}
	return e.extractJSON(ctx, obj)
}

func (e *Extractor) extractMultipart(body []byte, boundary string) ([]Turn, error) {
	if strings.TrimSpace(boundary) == "" {
		return nil, fmt.Errorf("multipart boundary is required")
	}
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	turn := Turn{Role: "user"}
	for ordinal := 0; ; ordinal++ {
		p, err := r.NextPart()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		data, err := readBounded(p, e.MaxPartBytes)
		_ = p.Close()
		if err != nil {
			return nil, err
		}
		kind := "text"
		if p.FileName() != "" {
			kind = kindForMIME(p.Header.Get("Content-Type"), "file")
		}
		part, err := e.makePart(kind, data, fmt.Sprintf("multipart.%s[%d]", p.FormName(), ordinal), p.FileName(), p.Header.Get("Content-Type"))
		if err != nil {
			return nil, err
		}
		turn.Parts = append(turn.Parts, part)
	}
	if len(turn.Parts) == 0 {
		return nil, nil
	}
	return []Turn{turn}, nil
}

func (e *Extractor) extractJSON(ctx context.Context, root map[string]any) ([]Turn, error) {
	turns := make([]Turn, 0)
	if system, ok := root["system"]; ok {
		parts, err := e.partsFromValue(ctx, system, "system")
		if err != nil {
			return nil, err
		}
		if len(parts) > 0 {
			turns = append(turns, Turn{Role: "system", Parts: parts})
		}
	}
	if messages, ok := root["messages"].([]any); ok {
		for i, raw := range messages {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			parts, err := e.partsFromValue(ctx, m["content"], fmt.Sprintf("messages[%d].content", i))
			if err != nil {
				return nil, err
			}
			if attachments, ok := m["attachments"]; ok {
				attachmentParts, err := e.partsFromValue(ctx, attachments, fmt.Sprintf("messages[%d].attachments", i))
				if err != nil {
					return nil, err
				}
				parts = append(parts, attachmentParts...)
			}
			if len(parts) > 0 {
				turns = append(turns, Turn{Role: stringValue(m["role"], "user"), Parts: parts})
			}
		}
	}
	if contents, ok := root["contents"].([]any); ok {
		for i, raw := range contents {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			parts, err := e.partsFromValue(ctx, m["parts"], fmt.Sprintf("contents[%d].parts", i))
			if err != nil {
				return nil, err
			}
			role := stringValue(m["role"], "user")
			if role == "model" {
				role = "assistant"
			}
			if len(parts) > 0 {
				turns = append(turns, Turn{Role: role, Parts: parts})
			}
		}
	}
	if input, ok := root["input"]; ok {
		switch v := input.(type) {
		case string:
			part, err := e.makePart("text", []byte(v), "input", "", "text/plain; charset=utf-8")
			if err != nil {
				return nil, err
			}
			turns = append(turns, Turn{Role: "user", Parts: []Part{part}})
		case []any:
			for i, item := range v {
				if m, ok := item.(map[string]any); ok && stringValue(m["type"], "") == "message" {
					parts, err := e.partsFromValue(ctx, m["content"], fmt.Sprintf("input[%d].content", i))
					if err != nil {
						return nil, err
					}
					if len(parts) > 0 {
						turns = append(turns, Turn{Role: stringValue(m["role"], "user"), Parts: parts})
					}
					continue
				}
				parts, err := e.partsFromValue(ctx, item, fmt.Sprintf("input[%d]", i))
				if err != nil {
					return nil, err
				}
				if len(parts) > 0 {
					turns = append(turns, Turn{Role: "user", Parts: parts})
				}
			}
		}
	}
	if len(turns) == 0 {
		for _, key := range []string{"prompt", "query", "text"} {
			if value, ok := root[key]; ok {
				parts, err := e.partsFromValue(ctx, value, key)
				if err != nil {
					return nil, err
				}
				if len(parts) > 0 {
					turns = append(turns, Turn{Role: "user", Parts: parts})
					break
				}
			}
		}
	}
	return turns, nil
}

func (e *Extractor) partsFromValue(ctx context.Context, value any, path string) ([]Part, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case string:
		p, err := e.makePart("text", []byte(v), path, "", "text/plain; charset=utf-8")
		return []Part{p}, err
	case []any:
		out := make([]Part, 0, len(v))
		for i, item := range v {
			parts, err := e.partsFromValue(ctx, item, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out = append(out, parts...)
		}
		return out, nil
	case map[string]any:
		return e.partsFromMap(ctx, v, path)
	default:
		data, _ := json.Marshal(v)
		p, err := e.makePart("text", data, path, "", "application/json")
		return []Part{p}, err
	}
}

func (e *Extractor) partsFromMap(ctx context.Context, m map[string]any, path string) ([]Part, error) {
	m = withoutEncryptedContent(m)
	typ := strings.ToLower(stringValue(m["type"], ""))
	if text, ok := m["text"].(string); ok {
		p, err := e.makePart("text", []byte(text), path+".text", "", "text/plain; charset=utf-8")
		return []Part{p}, err
	}
	for _, key := range []string{"inlineData", "inline_data"} {
		if dataMap, ok := m[key].(map[string]any); ok {
			return e.base64Part(stringValue(dataMap["data"], ""), kindForMIME(stringValue(firstValue(dataMap, "mimeType", "mime_type"), ""), "file"), path+"."+key, "", stringValue(firstValue(dataMap, "mimeType", "mime_type"), ""))
		}
	}
	if source, ok := m["source"].(map[string]any); ok && strings.EqualFold(stringValue(source["type"], ""), "base64") {
		mimeType := stringValue(firstValue(source, "media_type", "mimeType"), "")
		return e.base64Part(stringValue(source["data"], ""), kindForMIME(mimeType, mediaKindFromType(typ)), path+".source.data", stringValue(m["filename"], ""), mimeType)
	}
	if source, ok := m["source"].(map[string]any); ok && strings.EqualFold(stringValue(source["type"], ""), "url") {
		mimeType := stringValue(firstValue(source, "media_type", "mimeType"), "")
		return e.encodedOrRemotePart(ctx, stringValue(source["url"], ""), kindForMIME(mimeType, mediaKindFromType(typ)), path+".source.url", stringValue(m["filename"], ""), mimeType)
	}
	if raw, ok := m["file_data"].(string); ok && raw != "" {
		lower := strings.ToLower(raw)
		if !strings.HasPrefix(lower, "data:") && !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			return e.base64Part(raw, "file", path+".file_data", stringValue(m["filename"], ""), stringValue(firstValue(m, "mime_type", "media_type"), ""))
		}
		return e.encodedOrRemotePart(ctx, raw, "file", path+".file_data", stringValue(m["filename"], ""), stringValue(firstValue(m, "mime_type", "media_type"), ""))
	}
	if raw, ok := m["image_url"]; ok {
		if nested, ok := raw.(map[string]any); ok {
			raw = nested["url"]
		}
		if s, ok := raw.(string); ok {
			return e.encodedOrRemotePart(ctx, s, "image", path+".image_url", "", "")
		}
	}
	if raw, ok := m["file_url"]; ok {
		if nested, ok := raw.(map[string]any); ok {
			raw = nested["url"]
		}
		if value, ok := raw.(string); ok {
			return e.encodedOrRemotePart(ctx, value, "file", path+".file_url", stringValue(m["filename"], ""), "")
		}
	}
	if nested, ok := m["input_audio"].(map[string]any); ok {
		if data := stringValue(nested["data"], ""); data != "" {
			mimeType := "audio/" + stringValue(nested["format"], "octet-stream")
			return e.base64Part(data, "file", path+".input_audio.data", stringValue(m["filename"], ""), mimeType)
		}
	}
	if raw, ok := m["url"].(string); ok && (strings.Contains(typ, "image") || strings.Contains(typ, "file")) {
		return e.encodedOrRemotePart(ctx, raw, mediaKindFromType(typ), path+".url", stringValue(m["filename"], ""), stringValue(firstValue(m, "mime_type", "media_type"), ""))
	}
	for _, key := range []string{"fileData", "file_data"} {
		if nested, ok := m[key].(map[string]any); ok {
			raw := stringValue(firstValue(nested, "fileUri", "file_uri"), "")
			mimeType := stringValue(firstValue(nested, "mimeType", "mime_type"), "")
			return e.encodedOrRemotePart(ctx, raw, kindForMIME(mimeType, "file"), path+"."+key, stringValue(nested["displayName"], ""), mimeType)
		}
	}
	if id := stringValue(firstValue(m, "file_id", "fileId"), ""); id != "" {
		return nil, fmt.Errorf("%w: %s", ErrOpaqueFileReference, path)
	}
	if content, ok := m["content"]; ok {
		return e.partsFromValue(ctx, content, path+".content")
	}
	data, _ := json.Marshal(m)
	p, err := e.makePart("text", data, path, "", "application/json")
	return []Part{p}, err
}

func withoutEncryptedContent(m map[string]any) map[string]any {
	if _, ok := m["encrypted_content"]; !ok {
		return m
	}
	clean := make(map[string]any, len(m)-1)
	for key, value := range m {
		if key != "encrypted_content" {
			clean[key] = value
		}
	}
	return clean
}

func (e *Extractor) base64Part(raw, kind, path, filename, declaredMIME string) ([]Part, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(raw))
	}
	if err != nil {
		return nil, fmt.Errorf("decode base64 at %s: %w", path, err)
	}
	p, err := e.makePart(kind, data, path, filename, declaredMIME)
	return []Part{p}, err
}

func (e *Extractor) encodedOrRemotePart(ctx context.Context, raw, kind, path, filename, declaredMIME string) ([]Part, error) {
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		data, mediaType, err := decodeDataURI(raw)
		if err != nil {
			return nil, err
		}
		p, err := e.makePart(kindForMIME(mediaType, kind), data, path, filename, mediaType)
		return []Part{p}, err
	}
	if strings.HasPrefix(strings.ToLower(raw), "http://") || strings.HasPrefix(strings.ToLower(raw), "https://") {
		if e.Fetcher == nil {
			return nil, fmt.Errorf("%w: %s", ErrRemoteDownloadOff, path)
		}
		data, mediaType, err := e.Fetcher.Fetch(ctx, raw, e.MaxPartBytes)
		if err != nil {
			return nil, err
		}
		p, err := e.makePart(kindForMIME(mediaType, kind), data, path, filename, firstNonEmpty(declaredMIME, mediaType))
		return []Part{p}, err
	}
	return nil, fmt.Errorf("%w: %s", ErrOpaqueFileReference, path)
}

func (e *Extractor) makePart(kind string, data []byte, path, filename, declaredMIME string) (Part, error) {
	if e.MaxPartBytes > 0 && int64(len(data)) > e.MaxPartBytes {
		return Part{}, ErrPartTooLarge
	}
	detected := http.DetectContentType(data)
	if kind == "text" && strings.TrimSpace(declaredMIME) != "application/json" {
		detected = "text/plain; charset=utf-8"
	}
	return Part{Kind: kind, Data: data, SourcePath: path, OriginalFilename: filename, DeclaredMIME: declaredMIME, DetectedMIME: detected, SHA256: hashBytes(data)}, nil
}

func decodeDataURI(raw string) ([]byte, string, error) {
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return nil, "", fmt.Errorf("invalid data URI")
	}
	meta, payload := raw[5:comma], raw[comma+1:]
	parts := strings.Split(meta, ";")
	mediaType := parts[0]
	if mediaType == "" {
		mediaType = "text/plain;charset=US-ASCII"
	}
	if len(parts) > 1 && strings.EqualFold(parts[len(parts)-1], "base64") {
		data, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			data, err = base64.RawStdEncoding.DecodeString(payload)
		}
		return data, mediaType, err
	}
	decoded, err := url.PathUnescape(payload)
	return []byte(decoded), mediaType, err
}

func readBounded(r *multipart.Part, max int64) ([]byte, error) {
	limit := max + 1
	if max <= 0 {
		limit = 64<<20 + 1
	}
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, err
	}
	if max > 0 && int64(len(data)) > max {
		return nil, ErrPartTooLarge
	}
	return data, nil
}

func firstValue(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}
func stringValue(v any, fallback string) string {
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}
func mediaKindFromType(t string) string {
	if strings.Contains(t, "image") {
		return "image"
	}
	return "file"
}
func kindForMIME(mimeType, fallback string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/") {
		return "image"
	}
	return fallback
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
