package sessionarchive

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"mime/multipart"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractorPreservesRepeatedBinaryOccurrences(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nexact-binary")
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"` + uri + `"}},{"type":"image_url","image_url":{"url":"` + uri + `"}}]}]}`)
	turns, err := (&Extractor{MaxPartBytes: 1024}).Extract(context.Background(), body, "application/json")
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.Len(t, turns[0].Parts, 3)
	require.Equal(t, png, turns[0].Parts[1].Data)
	require.Equal(t, png, turns[0].Parts[2].Data)
	require.Equal(t, turns[0].Parts[1].SHA256, turns[0].Parts[2].SHA256)
	require.Equal(t, "image", turns[0].Parts[1].Kind)
}

func TestExtractorDecodesPlainBase64FileData(t *testing.T) {
	want := []byte{0, 1, 2, 3, 254, 255}
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_file","filename":"audit.bin","file_data":"` + base64.StdEncoding.EncodeToString(want) + `"}]}]}`)
	turns, err := (&Extractor{MaxPartBytes: 1024}).Extract(context.Background(), body, "application/json")
	require.NoError(t, err)
	require.Equal(t, want, turns[0].Parts[0].Data)
	require.Equal(t, "audit.bin", turns[0].Parts[0].OriginalFilename)
	require.Equal(t, "file", turns[0].Parts[0].Kind)
}

func TestExtractorMultipartPreservesPartOrderAndBytes(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("prompt", "first"))
	file, err := w.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	want := []byte("\x89PNG\x00raw")
	_, err = file.Write(want)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	turns, err := (&Extractor{MaxPartBytes: 1024}).Extract(context.Background(), body.Bytes(), w.FormDataContentType())
	require.NoError(t, err)
	require.Len(t, turns[0].Parts, 2)
	require.Equal(t, []byte("first"), turns[0].Parts[0].Data)
	require.Equal(t, want, turns[0].Parts[1].Data)
	require.Equal(t, "source.png", turns[0].Parts[1].OriginalFilename)
}

func TestExtractorRejectsOpaqueFileReference(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-secret"}]}]}`)
	_, err := (&Extractor{MaxPartBytes: 1024}).Extract(context.Background(), body, "application/json")
	require.ErrorIs(t, err, ErrOpaqueFileReference)
}

func TestSafeRemoteFetcherRejectsPrivateAndCredentialedURLs(t *testing.T) {
	fetcher := NewSafeRemoteFetcher(false)
	for _, raw := range []string{"http://example.com/x", "https://127.0.0.1/x", "https://user:pass@example.com/x"} {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		require.ErrorIs(t, fetcher.validateURL(u), ErrUnsafeRemoteURL, raw)
	}
}

func TestLongestCommonPrefixUsesTurnHashes(t *testing.T) {
	existing := []persistedTurn{{Hash: "a"}, {Hash: "b"}, {Hash: "old"}}
	incoming := []Turn{{Hash: "a"}, {Hash: "b"}, {Hash: "new"}}
	require.Equal(t, 2, longestCommonPrefix(existing, incoming))
}

func TestDataURILimitIsEnforced(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(make([]byte, 5)) + `"}}]}]}`)
	_, err := (&Extractor{MaxPartBytes: 4}).Extract(context.Background(), body, "application/json")
	require.True(t, errors.Is(err, ErrPartTooLarge))
}
