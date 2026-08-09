package sessionarchive

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashSessionIdentity(userID int64, kind, value string) string {
	return hashBytes([]byte(strconv.FormatInt(userID, 10) + "\x00" + kind + "\x00" + value))
}

func hashTurn(turn Turn) string {
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(turn.Role)))
	h.Write([]byte{0})
	for i := range turn.Parts {
		p := &turn.Parts[i]
		h.Write([]byte(p.Kind))
		h.Write([]byte{0})
		h.Write([]byte(p.SHA256))
		h.Write([]byte{0})
		h.Write([]byte(p.OriginalFilename))
		h.Write([]byte{0})
		h.Write([]byte(p.DeclaredMIME))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashSequence(turns []Turn) string {
	h := sha256.New()
	for i := range turns {
		h.Write([]byte(turns[i].Hash))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
