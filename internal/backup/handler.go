package backup

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/internal/audit"
	"github.com/432539/gpt2api/pkg/resp"
)

// Handler 提供 /api/admin/system/backup/* 接口。
type Handler struct {
	svc      *Service
	dao      *DAO
	auditDAO *audit.DAO
}

// NewHandler 构造。
func NewHandler(svc *Service, dao *DAO, auditDAO *audit.DAO) *Handler {
	return &Handler{svc: svc, dao: dao, auditDAO: auditDAO}
}

// ---- 请求体 ----

type createReq struct {
	IncludeData *bool `json:"include_data,omitempty"` // 默认 true
}

// ---- 接口 ----

// Create POST /api/admin/system/backup
func (h *Handler) Create(c *gin.Context) {
	var req createReq
	_ = c.ShouldBindJSON(&req)
	includeData := true
	if req.IncludeData != nil {
		includeData = *req.IncludeData
	}
	actor := uint64(0)

	f, err := h.svc.Create(c.Request.Context(), actor, TriggerManual, includeData)
	if err != nil {
		audit.Record(c, h.auditDAO, "system.backup.create.failed", "", gin.H{"error": err.Error()})
		resp.Internal(c, err.Error())
		return
	}
	audit.Record(c, h.auditDAO, "system.backup.create", f.BackupID,
		gin.H{"size": f.SizeBytes, "include_data": includeData})
	resp.OK(c, f)
}

// List GET /api/admin/system/backup
func (h *Handler) List(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	items, err := h.dao.List(c.Request.Context(), limit, offset)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	total, _ := h.dao.Count(c.Request.Context())
	resp.OK(c, gin.H{
		"items":         items,
		"total":         total,
		"allow_restore": h.svc.AllowRestore(),
		"max_upload_mb": h.svc.cfg.MaxUploadMB,
	})
}

// Download GET /api/admin/system/backup/:id/download
func (h *Handler) Download(c *gin.Context) {
	id := c.Param("id")
	if !backupIDRe.MatchString(id) {
		resp.BadRequest(c, "invalid backup id")
		return
	}
	fh, meta, err := h.svc.OpenForDownload(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			resp.NotFound(c, "backup not found")
			return
		}
		resp.Internal(c, err.Error())
		return
	}
	defer fh.Close()

	c.Writer.Header().Set("Content-Type", "application/gzip")
	c.Writer.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, meta.FileName))
	c.Writer.Header().Set("Content-Length", strconv.FormatInt(meta.SizeBytes, 10))
	c.Writer.Header().Set("X-Backup-SHA256", meta.SHA256)
	c.Status(http.StatusOK)
	http.ServeContent(c.Writer, c.Request, meta.FileName, meta.CreatedAt, fh)
	audit.Record(c, h.auditDAO, "system.backup.download", id, nil)
}

// Delete DELETE /api/admin/system/backup/:id。
func (h *Handler) Delete(c *gin.Context) {
	id := c.Param("id")
	if !backupIDRe.MatchString(id) {
		resp.BadRequest(c, "invalid backup id")
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			resp.NotFound(c, "backup not found")
			return
		}
		resp.Internal(c, err.Error())
		return
	}
	audit.Record(c, h.auditDAO, "system.backup.delete", id, nil)
	resp.OK(c, gin.H{"deleted": id})
}

// Restore POST /api/admin/system/backup/:id/restore。
// backup.allow_restore 必须为 true,执行前后都会落审计。
func (h *Handler) Restore(c *gin.Context) {
	if !h.svc.AllowRestore() {
		resp.Forbidden(c, "restore is disabled by config; set backup.allow_restore=true first")
		return
	}
	id := c.Param("id")
	if !backupIDRe.MatchString(id) {
		resp.BadRequest(c, "invalid backup id")
		return
	}
	audit.Record(c, h.auditDAO, "system.backup.restore.begin", id, nil)
	if err := h.svc.Restore(c.Request.Context(), id); err != nil {
		audit.Record(c, h.auditDAO, "system.backup.restore.failed", id, gin.H{"error": err.Error()})
		resp.Internal(c, err.Error())
		return
	}
	audit.Record(c, h.auditDAO, "system.backup.restore.success", id, nil)
	resp.OK(c, gin.H{"restored": id})
}

// Upload POST /api/admin/system/backup/upload。
// 上传 .sql.gz 文件(multipart/form-data,字段名 "file")。
func (h *Handler) Upload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.svc.MaxUploadBytes()+4096)
	fh, err := c.FormFile("file")
	if err != nil {
		resp.BadRequest(c, "file is required: "+err.Error())
		return
	}
	if fh.Size > h.svc.MaxUploadBytes() {
		resp.BadRequest(c, fmt.Sprintf("file exceeds %d MB", h.svc.cfg.MaxUploadMB))
		return
	}
	src, err := fh.Open()
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	defer src.Close()

	actor := uint64(0)
	f, err := h.svc.ImportUpload(c.Request.Context(), actor, fh.Filename, src)
	if err != nil {
		audit.Record(c, h.auditDAO, "system.backup.upload.failed", fh.Filename, gin.H{"error": err.Error()})
		resp.BadRequest(c, err.Error())
		return
	}
	audit.Record(c, h.auditDAO, "system.backup.upload", f.BackupID,
		gin.H{"orig_name": fh.Filename, "size": f.SizeBytes})
	resp.OK(c, f)
}
