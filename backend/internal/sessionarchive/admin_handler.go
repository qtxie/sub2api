package sessionarchive

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

type AdminHandler struct{ service *Service }

func NewAdminHandler(service *Service) *AdminHandler { return &AdminHandler{service: service} }

func (h *AdminHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	if pageSize > 200 {
		pageSize = 200
	}
	var userID int64
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid user_id")
			return
		}
		userID = id
	}
	items, total, err := h.service.ListSessions(c.Request.Context(), userID, pageSize, (page-1)*pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

func (h *AdminHandler) Get(c *gin.Context) {
	id, ok := archiveID(c)
	if !ok {
		return
	}
	item, err := h.service.GetSession(c.Request.Context(), id)
	if err != nil {
		archiveError(c, err)
		return
	}
	logger.LegacyPrintf("sessionarchive.admin", "session archive read admin_id=%d session_id=%d", adminID(c), id)
	response.Success(c, item)
}

func (h *AdminHandler) Download(c *gin.Context) {
	sessionID, ok := archiveID(c)
	if !ok {
		return
	}
	blobID, err := strconv.ParseInt(strings.TrimSpace(c.Param("blob_id")), 10, 64)
	if err != nil || blobID <= 0 {
		response.BadRequest(c, "Invalid blob id")
		return
	}
	reader, length, mimeType, filename, err := h.service.OpenBlob(c.Request.Context(), sessionID, blobID)
	if err != nil {
		archiveError(c, err)
		return
	}
	defer reader.Close()
	filename = safeFilename(filename)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	logger.LegacyPrintf("sessionarchive.admin", "session archive blob read admin_id=%d session_id=%d blob_id=%d", adminID(c), sessionID, blobID)
	c.DataFromReader(http.StatusOK, length, mimeType, reader, nil)
}

func (h *AdminHandler) Export(c *gin.Context) {
	id, ok := archiveID(c)
	if !ok {
		return
	}
	detail, err := h.service.GetSession(c.Request.Context(), id)
	if err != nil {
		archiveError(c, err)
		return
	}
	reader, writer := io.Pipe()
	go func() {
		zw := zip.NewWriter(writer)
		manifest, err := zw.Create("manifest.json")
		if err == nil {
			var data []byte
			data, err = json.MarshalIndent(detail, "", "  ")
			if err == nil {
				_, err = manifest.Write(data)
			}
		}
		if err == nil {
			for _, turn := range detail.Turns {
				for _, part := range turn.Parts {
					if part.Kind == "text" {
						continue
					}
					var src io.ReadCloser
					src, _, _, _, err = h.service.OpenBlob(c.Request.Context(), id, part.BlobID)
					if err != nil {
						break
					}
					name := fmt.Sprintf("files/turn-%04d-part-%04d-%s", turn.Ordinal, part.Ordinal, safeFilename(part.OriginalFilename))
					var dst io.Writer
					dst, err = zw.Create(name)
					if err == nil {
						_, err = io.Copy(dst, src)
					}
					_ = src.Close()
					if err != nil {
						break
					}
				}
				if err != nil {
					break
				}
			}
		}
		closeErr := zw.Close()
		if err == nil {
			err = closeErr
		}
		_ = writer.CloseWithError(err)
	}()
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="session-%d.zip"`, id))
	logger.LegacyPrintf("sessionarchive.admin", "session archive exported admin_id=%d session_id=%d", adminID(c), id)
	c.DataFromReader(http.StatusOK, -1, "application/zip", reader, nil)
}

func (h *AdminHandler) Delete(c *gin.Context) {
	id, ok := archiveID(c)
	if !ok {
		return
	}
	if err := h.service.DeleteSession(c.Request.Context(), id); err != nil {
		archiveError(c, err)
		return
	}
	logger.LegacyPrintf("sessionarchive.admin", "session archive deleted admin_id=%d session_id=%d", adminID(c), id)
	response.Success(c, gin.H{"deleted": true})
}

func archiveID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid session id")
		return 0, false
	}
	return id, true
}
func archiveError(c *gin.Context, err error) {
	if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrBlobNotFound) {
		response.NotFound(c, err.Error())
		return
	}
	response.ErrorFrom(c, err)
}
func adminID(c *gin.Context) int64 {
	if v, ok := c.Get("admin_id"); ok {
		switch id := v.(type) {
		case int64:
			return id
		case int:
			return int64(id)
		}
	}
	return 0
}
func safeFilename(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		if r < ' ' || r == '\x7f' || r == '"' || r == '\\' || r == '/' {
			return '-'
		}
		return r
	}, value)
	if value == "" || value == "." {
		return "content.bin"
	}
	return value
}
